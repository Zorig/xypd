package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"syscall/js"
)

// Constants - no more magic numbers
const (
	chunkSize        = 16 * 1024         // 16KB per chunk
	maxBufferSize    = 64 * 1024         // 64KB buffer limit (reduced from 16MB to prevent queue overflow)
	backpressureWait = 50                // milliseconds (faster retry)
	maxFileSize      = 500 * 1024 * 1024 // 500MB max file size
	roomIDLength     = 6
)

var (
	document = js.Global().Get("document")
	window   = js.Global().Get("window")
	ws       js.Value
	pc       js.Value
	dc       js.Value
	roomID   string
	isSender bool

	// Track js.Func for cleanup
	wsOnOpen           js.Func
	wsOnMessage        js.Func
	wsOnError          js.Func
	wsOnClose          js.Func
	pcOnIceCandidate   js.Func
	pcOnDataChannel    js.Func
	pcOnIceStateChange js.Func
	dcOnOpen           js.Func
	dcOnMessage        js.Func
)

func main() {
	c := make(chan struct{}, 0)

	// Register callbacks
	js.Global().Set("connectToRoom", js.FuncOf(connectToRoom))
	js.Global().Set("createSession", js.FuncOf(createSession))
	js.Global().Set("sendFile", js.FuncOf(sendFile))

	<-c
}

// generateSecureRoomID uses crypto/rand for cryptographically secure room IDs
func generateSecureRoomID() (string, error) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	id := make([]byte, roomIDLength)
	for i := range id {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		id[i] = chars[idx.Int64()]
	}
	return string(id), nil
}

func createSession(this js.Value, args []js.Value) interface{} {
	rID, err := generateSecureRoomID()
	if err != nil {
		displayError("Failed to generate secure room ID")
		return nil
	}

	document.Call("getElementById", "generated-room-id").Set("innerText", rID)

	go initiateConnection(rID)

	return nil
}

func initiateConnection(rID string) {
	roomID = rID
	isSender = true
	fmt.Println("Creating session:", roomID)
	updateStatus("Waiting for peer...")

	createWebSocketConnection(func() {
		fmt.Println("WebSocket Connected")
		msg := map[string]string{
			"type":   "join",
			"roomId": roomID,
		}
		sendSignal(msg)
	})
}

func connectToRoom(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		displayError("Room ID is required.")
		return nil
	}
	roomID = args[0].String()
	roomID = strings.ToUpper(roomID)
	isSender = false

	fmt.Println("Connecting to room:", roomID)
	updateStatus("Connecting...")

	createWebSocketConnection(func() {
		fmt.Println("WebSocket Connected")
		msg := map[string]string{
			"type":   "join",
			"roomId": roomID,
		}
		sendSignal(msg)

		document.Call("getElementById", "receiver-view").Get("style").Set("display", "none")
		document.Call("getElementById", "transfer-container").Get("style").Set("display", "block")

		setupWebRTC()
	})

	return nil
}

// createWebSocketConnection extracts common WebSocket setup logic
func createWebSocketConnection(onConnect func()) {
	// Cleanup previous WebSocket handlers if they exist
	cleanupWebSocketHandlers()

	protocol := "ws://"
	if window.Get("location").Get("protocol").String() == "https:" {
		protocol = "wss://"
	}
	wsURL := protocol + window.Get("location").Get("host").String() + "/ws"
	ws = js.Global().Get("WebSocket").New(wsURL)

	wsOnOpen = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		onConnect()
		return nil
	})
	ws.Set("onopen", wsOnOpen)

	wsOnMessage = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		evt := args[0]
		data := evt.Get("data").String()
		handleSignalMessage(data)
		return nil
	})
	ws.Set("onmessage", wsOnMessage)

	wsOnError = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fmt.Println("WebSocket Error")
		displayError("Connection failed. Please try again.")
		return nil
	})
	ws.Set("onerror", wsOnError)

	wsOnClose = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fmt.Println("WebSocket Closed")
		updateStatus("Disconnected from server")
		return nil
	})
	ws.Set("onclose", wsOnClose)
}

func cleanupWebSocketHandlers() {
	if wsOnOpen.Truthy() {
		wsOnOpen.Release()
	}
	if wsOnMessage.Truthy() {
		wsOnMessage.Release()
	}
	if wsOnError.Truthy() {
		wsOnError.Release()
	}
	if wsOnClose.Truthy() {
		wsOnClose.Release()
	}
}

func cleanupWebRTCHandlers() {
	if pcOnIceCandidate.Truthy() {
		pcOnIceCandidate.Release()
	}
	if pcOnDataChannel.Truthy() {
		pcOnDataChannel.Release()
	}
	if pcOnIceStateChange.Truthy() {
		pcOnIceStateChange.Release()
	}
	if dcOnOpen.Truthy() {
		dcOnOpen.Release()
	}
	if dcOnMessage.Truthy() {
		dcOnMessage.Release()
	}
}

// safeGetString safely extracts a string from map with type checking
func safeGetString(m map[string]interface{}, key string) (string, bool) {
	val, exists := m[key]
	if !exists {
		return "", false
	}
	str, ok := val.(string)
	return str, ok
}

// safeGetMap safely extracts a nested map with type checking
func safeGetMap(m map[string]interface{}, key string) (map[string]interface{}, bool) {
	val, exists := m[key]
	if !exists {
		return nil, false
	}
	nested, ok := val.(map[string]interface{})
	return nested, ok
}

// safeGetFloat safely extracts a float64 from map with type checking
func safeGetFloat(m map[string]interface{}, key string) (float64, bool) {
	val, exists := m[key]
	if !exists {
		return 0, false
	}
	f, ok := val.(float64)
	return f, ok
}

func handleSignalMessage(data string) {
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		fmt.Println("Invalid JSON received:", err)
		return
	}

	msgType, ok := safeGetString(msg, "type")
	if !ok {
		fmt.Println("Missing or invalid 'type' field in message")
		return
	}

	switch msgType {
	case "joined":
		if !isSender {
			showTransferUI()
		}

	case "error":
		// Handle server errors
		if payload, ok := msg["payload"].(string); ok {
			displayError(payload)
		} else {
			displayError("Server error occurred")
		}

	case "offer":
		showTransferUI()

		payload, ok := safeGetMap(msg, "payload")
		if !ok {
			fmt.Println("Invalid offer payload")
			return
		}

		sdp, ok := safeGetString(payload, "sdp")
		if !ok {
			fmt.Println("Invalid SDP in offer")
			return
		}

		offerType, ok := safeGetString(payload, "type")
		if !ok {
			fmt.Println("Invalid type in offer")
			return
		}

		remoteDesc := map[string]interface{}{
			"sdp":  sdp,
			"type": offerType,
		}
		pc.Call("setRemoteDescription", remoteDesc)

		answerHandler := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			answer := args[0]
			pc.Call("setLocalDescription", answer)

			resp := map[string]interface{}{
				"type": "answer",
				"payload": map[string]interface{}{
					"sdp":  answer.Get("sdp").String(),
					"type": answer.Get("type").String(),
				},
				"roomId": roomID,
			}
			sendSignal(resp)
			return nil
		})
		// Note: This function is released by the Promise
		pc.Call("createAnswer").Call("then", answerHandler)

	case "answer":
		showTransferUI()

		payload, ok := safeGetMap(msg, "payload")
		if !ok {
			fmt.Println("Invalid answer payload")
			return
		}

		sdp, ok := safeGetString(payload, "sdp")
		if !ok {
			fmt.Println("Invalid SDP in answer")
			return
		}

		answerType, ok := safeGetString(payload, "type")
		if !ok {
			fmt.Println("Invalid type in answer")
			return
		}

		remoteDesc := map[string]interface{}{
			"sdp":  sdp,
			"type": answerType,
		}
		pc.Call("setRemoteDescription", remoteDesc)

	case "peer_joined":
		showTransferUI()
		createOffer()

	case "candidate":
		payload, ok := safeGetMap(msg, "payload")
		if !ok {
			fmt.Println("Invalid candidate payload")
			return
		}

		candidate := map[string]interface{}{
			"candidate":     payload["candidate"],
			"sdpMid":        payload["sdpMid"],
			"sdpMLineIndex": payload["sdpMLineIndex"],
		}
		pc.Call("addIceCandidate", candidate)
	}
}

func showTransferUI() {
	if document.Call("getElementById", "transfer-container").Get("style").Get("display").String() == "none" {
		document.Call("getElementById", "join-container").Get("style").Set("display", "none")
		document.Call("getElementById", "sender-view").Get("style").Set("display", "none")
		document.Call("getElementById", "receiver-view").Get("style").Set("display", "none")
		document.Call("getElementById", "transfer-container").Get("style").Set("display", "block")
		setupWebRTC()
	}
}

func sendSignal(msg interface{}) {
	jsonMsg, err := json.Marshal(msg)
	if err != nil {
		fmt.Println("Failed to marshal signal message:", err)
		return
	}
	ws.Call("send", string(jsonMsg))
}

func setupWebRTC() {
	// Cleanup previous handlers
	cleanupWebRTCHandlers()

	config := map[string]interface{}{
		"iceServers": []interface{}{
			map[string]interface{}{
				"urls": "stun:stun.l.google.com:19302",
			},
			map[string]interface{}{
				"urls": "stun:stun1.l.google.com:19302",
			},
		},
	}
	pc = js.Global().Get("RTCPeerConnection").New(config)

	pcOnIceCandidate = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		evt := args[0]
		if !evt.Get("candidate").IsNull() {
			candidate := evt.Get("candidate").Get("candidate").String()
			sdpMid := evt.Get("candidate").Get("sdpMid").String()
			sdpMLineIndex := evt.Get("candidate").Get("sdpMLineIndex").Int()

			msg := map[string]interface{}{
				"type": "candidate",
				"payload": map[string]interface{}{
					"candidate":     candidate,
					"sdpMid":        sdpMid,
					"sdpMLineIndex": sdpMLineIndex,
				},
				"roomId": roomID,
			}
			sendSignal(msg)
		}
		return nil
	})
	pc.Set("onicecandidate", pcOnIceCandidate)

	pcOnDataChannel = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fmt.Println("Data Channel Received")
		dc = args[0].Get("channel")
		setupDataChannel(dc)
		return nil
	})
	pc.Set("ondatachannel", pcOnDataChannel)

	// Add ICE connection state monitoring
	pcOnIceStateChange = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		state := pc.Get("iceConnectionState").String()
		fmt.Println("ICE Connection State:", state)

		switch state {
		case "connected":
			updateStatus("Connected! Ready to transfer.")
		case "failed":
			updateStatus("Connection failed. Please try again.")
			displayError("Failed to establish peer connection. Try refreshing the page.")
		case "disconnected":
			updateStatus("Peer disconnected")
		case "closed":
			updateStatus("Connection closed")
			cleanupWebRTCHandlers()
		}
		return nil
	})
	pc.Set("oniceconnectionstatechange", pcOnIceStateChange)
}

func createOffer() {
	dc = pc.Call("createDataChannel", "fileTransfer")
	setupDataChannel(dc)

	offerHandler := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		offer := args[0]
		pc.Call("setLocalDescription", offer)

		msg := map[string]interface{}{
			"type": "offer",
			"payload": map[string]interface{}{
				"sdp":  offer.Get("sdp").String(),
				"type": offer.Get("type").String(),
			},
			"roomId": roomID,
		}
		sendSignal(msg)
		return nil
	})
	pc.Call("createOffer").Call("then", offerHandler)
}

func setupDataChannel(channel js.Value) {
	// Cleanup previous handlers
	if dcOnOpen.Truthy() {
		dcOnOpen.Release()
	}
	if dcOnMessage.Truthy() {
		dcOnMessage.Release()
	}

	dcOnOpen = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fmt.Println("Data Channel Open")
		updateStatus("Connected! Ready to transfer.")
		return nil
	})
	channel.Set("onopen", dcOnOpen)

	var incomingFileMeta map[string]interface{}
	var receivedBytes int
	var receivedChunks []js.Value

	dcOnMessage = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		data := args[0].Get("data")

		if data.Type() == js.TypeString {
			var meta map[string]interface{}
			err := json.Unmarshal([]byte(data.String()), &meta)
			if err != nil {
				fmt.Println("Failed to parse metadata:", err)
				return nil
			}

			metaType, ok := safeGetString(meta, "type")
			if ok && metaType == "metadata" {
				fmt.Println("Received metadata:", meta)
				incomingFileMeta = meta
				receivedBytes = 0
				receivedChunks = make([]js.Value, 0)

				name, _ := safeGetString(meta, "name")
				updateStatus(fmt.Sprintf("Receiving %s...", name))
				updateProgress(0)
				document.Call("getElementById", "progress-container").Get("style").Set("display", "block")
			}
			return nil
		}

		if data.Get("byteLength").IsUndefined() {
			return nil
		}

		byteLength := data.Get("byteLength").Int()
		receivedBytes += byteLength
		receivedChunks = append(receivedChunks, data)

		totalSize, ok := safeGetFloat(incomingFileMeta, "size")
		if !ok {
			fmt.Println("Invalid file size in metadata")
			return nil
		}

		progress := int(float64(receivedBytes) / totalSize * 100)
		updateProgress(progress)

		if receivedBytes >= int(totalSize) {
			fmt.Println("File received completely!")

			// Reassemble file
			array := js.Global().Get("Array").New()
			for _, chunk := range receivedChunks {
				array.Call("push", chunk)
			}

			blobType := "application/octet-stream"
			if ft, ok := safeGetString(incomingFileMeta, "fileType"); ok && ft != "" {
				blobType = ft
			}

			blobOptions := map[string]interface{}{
				"type": blobType,
			}
			blob := js.Global().Get("Blob").New(array, blobOptions)

			url := js.Global().Get("URL").Call("createObjectURL", blob)
			a := document.Call("createElement", "a")
			a.Set("href", url)

			filename := "received_file"
			if fn, ok := safeGetString(incomingFileMeta, "name"); ok {
				filename = fn
			}
			a.Set("download", filename)

			document.Get("body").Call("appendChild", a)
			a.Call("click")
			document.Get("body").Call("removeChild", a)
			js.Global().Get("URL").Call("revokeObjectURL", url)

			updateStatus(fmt.Sprintf("Received %s!", filename))
			document.Call("getElementById", "progress-container").Get("style").Set("display", "none")

			incomingFileMeta = nil
			receivedChunks = nil
			receivedBytes = 0
		}
		return nil
	})
	channel.Set("onmessage", dcOnMessage)
}

func sendFile(this js.Value, args []js.Value) interface{} {
	fileInput := document.Call("getElementById", "fileInput")
	files := fileInput.Get("files")
	if files.Length() == 0 {
		return nil
	}

	if dc.IsUndefined() || dc.Get("readyState").String() != "open" {
		createOffer()
		updateStatus("Connecting... please wait")
		return nil
	}

	file := files.Index(0)
	name := file.Get("name").String()
	fileType := file.Get("type").String()
	size := file.Get("size").Int()

	// Check file size limit
	if size > maxFileSize {
		displayError(fmt.Sprintf("File too large. Maximum size: %d MB", maxFileSize/(1024*1024)))
		return nil
	}

	meta := map[string]interface{}{
		"type":     "metadata",
		"name":     name,
		"fileType": fileType,
		"size":     size,
	}
	jsonMeta, err := json.Marshal(meta)
	if err != nil {
		fmt.Println("Failed to marshal file metadata:", err)
		displayError("Failed to prepare file for transfer")
		return nil
	}
	dc.Call("send", string(jsonMeta))

	document.Call("getElementById", "progress-container").Get("style").Set("display", "block")
	updateProgress(0)

	// Chunked reading and sending
	offset := 0
	reader := js.Global().Get("FileReader").New()

	var readNextChunk js.Func
	readNextChunk = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		// Check if data channel is still open
		if dc.IsUndefined() || dc.Get("readyState").String() != "open" {
			updateStatus("Transfer interrupted - peer disconnected")
			readNextChunk.Release()
			return nil
		}

		// Check bufferedAmount to avoid overwhelming the channel
		// Safari has a lower limit, so we keep this tight
		if dc.Get("bufferedAmount").Int() > maxBufferSize {
			window.Call("setTimeout", readNextChunk, backpressureWait)
			return nil
		}

		if offset >= size {
			updateStatus(fmt.Sprintf("Sent %s!", name))
			document.Call("getElementById", "progress-container").Get("style").Set("display", "none")
			readNextChunk.Release()
			return nil
		}

		end := offset + chunkSize
		if end > size {
			end = size
		}

		blob := file.Call("slice", offset, end)

		var onLoadHandler js.Func
		onLoadHandler = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			// Double-check channel state and buffer before actual send
			if dc.IsUndefined() || dc.Get("readyState").String() != "open" {
				onLoadHandler.Release()
				return nil
			}

			// Final safety check for buffer
			if dc.Get("bufferedAmount").Int() > maxBufferSize {
				// Buffer full? Back off and retry this chunk loop
				// We don't increment offset, just retry
				onLoadHandler.Release()
				window.Call("setTimeout", readNextChunk, backpressureWait)
				return nil
			}

			res := reader.Get("result")
			dc.Call("send", res)

			offset = end
			progress := int(float64(offset) / float64(size) * 100)
			updateProgress(progress)

			// Schedule next chunk using setTimeout to yield to event loop
			window.Call("setTimeout", readNextChunk, 0)
			onLoadHandler.Release()
			return nil
		})
		reader.Set("onload", onLoadHandler)
		reader.Call("readAsArrayBuffer", blob)
		return nil
	})

	readNextChunk.Invoke()
	return nil
}

func updateProgress(percent int) {
	document.Call("getElementById", "progress-bar").Get("style").Set("width", fmt.Sprintf("%d%%", percent))
	document.Call("getElementById", "progress-text").Set("innerText", fmt.Sprintf("%d%%", percent))
}

func updateStatus(msg string) {
	document.Call("getElementById", "status").Set("innerText", msg)
}

func displayError(msg string) {
	window.Call("alert", msg)
}
