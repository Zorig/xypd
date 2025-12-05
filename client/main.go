package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"syscall/js"
	"time"
)

var (
	document = js.Global().Get("document")
	window   = js.Global().Get("window")
	ws       js.Value
	pc       js.Value
	dc       js.Value
	roomID   string
	isSender bool
)

func main() {
	c := make(chan struct{}, 0)

	// Register callbacks
	js.Global().Set("connectToRoom", js.FuncOf(connectToRoom))
	js.Global().Set("createSession", js.FuncOf(createSession))
	js.Global().Set("sendFile", js.FuncOf(sendFile))

	<-c
}

func createSession(this js.Value, args []js.Value) interface{} {
	rand.Seed(time.Now().UnixNano())

	chars := "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	id := make([]byte, 6)
	for i := range id {
		id[i] = chars[rand.Intn(len(chars))]
	}
	rID := string(id)

	document.Call("getElementById", "generated-room-id").Set("innerText", rID)

	go initiateConnection(rID)

	return nil
}

func initiateConnection(rID string) {
	roomID = rID
	isSender = true // Set flag
	fmt.Println("Creating session:", roomID)
	updateStatus("Waiting for peer...")

	protocol := "ws://"
	if window.Get("location").Get("protocol").String() == "https:" {
		protocol = "wss://"
	}
	wsURL := protocol + window.Get("location").Get("host").String() + "/ws"
	ws = js.Global().Get("WebSocket").New(wsURL)

	ws.Set("onopen", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fmt.Println("WebSocket Connected")
		msg := map[string]string{
			"type":   "join",
			"roomId": roomID,
		}
		sendSignal(msg)
		return nil
	}))

	ws.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		evt := args[0]
		data := evt.Get("data").String()
		handleSignalMessage(data)
		return nil
	}))
}

func connectToRoom(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		displayError("Room ID is required.")
		return nil
	}
	roomID = args[0].String()
	roomID = strings.ToUpper(roomID)
	isSender = false // Set flag

	fmt.Println("Connecting to room:", roomID)
	updateStatus("Connecting...")

	protocol := "ws://"
	if window.Get("location").Get("protocol").String() == "https:" {
		protocol = "wss://"
	}
	wsURL := protocol + window.Get("location").Get("host").String() + "/ws"
	ws = js.Global().Get("WebSocket").New(wsURL)

	ws.Set("onopen", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fmt.Println("WebSocket Connected")
		msg := map[string]string{
			"type":   "join",
			"roomId": roomID,
		}
		sendSignal(msg)

		document.Call("getElementById", "receiver-view").Get("style").Set("display", "none")
		document.Call("getElementById", "transfer-container").Get("style").Set("display", "block")

		setupWebRTC()
		return nil
	}))

	ws.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		evt := args[0]
		data := evt.Get("data").String()
		handleSignalMessage(data)
		return nil
	}))

	return nil
}

func handleSignalMessage(data string) {
	var msg map[string]interface{}
	json.Unmarshal([]byte(data), &msg)

	msgType := msg["type"].(string)

	switch msgType {
	case "joined":
		// Only show transfer UI if we are the receiver (joining an existing room)
		if !isSender {
			showTransferUI()
		}

	case "offer":
		// ...
		showTransferUI()

		payload := msg["payload"].(map[string]interface{})
		sdp := payload["sdp"].(string)
		offerType := payload["type"].(string)

		remoteDesc := map[string]interface{}{
			"sdp":  sdp,
			"type": offerType,
		}
		pc.Call("setRemoteDescription", remoteDesc)

		pc.Call("createAnswer").Call("then", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
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
		}))

	case "answer":
		showTransferUI()
		payload := msg["payload"].(map[string]interface{})
		sdp := payload["sdp"].(string)
		answerType := payload["type"].(string)

		remoteDesc := map[string]interface{}{
			"sdp":  sdp,
			"type": answerType,
		}
		pc.Call("setRemoteDescription", remoteDesc)

	case "peer_joined":
		showTransferUI()
		// Auto-connect so the user doesn't have to click Send twice
		createOffer()

	case "candidate":
		payload := msg["payload"].(map[string]interface{})
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
	jsonMsg, _ := json.Marshal(msg)
	ws.Call("send", string(jsonMsg))
}

func setupWebRTC() {
	config := map[string]interface{}{
		"iceServers": []interface{}{
			map[string]interface{}{
				"urls": "stun:stun.l.google.com:19302",
			},
		},
	}
	pc = js.Global().Get("RTCPeerConnection").New(config)

	pc.Set("onicecandidate", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
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
	}))

	pc.Set("ondatachannel", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fmt.Println("Data Channel Received")
		dc = args[0].Get("channel")
		setupDataChannel(dc)
		return nil
	}))
}

func createOffer() {
	dc = pc.Call("createDataChannel", "fileTransfer")
	setupDataChannel(dc)

	pc.Call("createOffer").Call("then", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
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
	}))
}

const chunkSize = 16 * 1024 // 16KB

func setupDataChannel(channel js.Value) {
	channel.Set("onopen", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fmt.Println("Data Channel Open")
		updateStatus("Connected! Ready to transfer.")
		return nil
	}))

	var incomingFileMeta map[string]interface{}
	var receivedBytes int
	var receivedChunks []js.Value

	channel.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		data := args[0].Get("data")

		if data.Type() == js.TypeString {
			var meta map[string]interface{}
			err := json.Unmarshal([]byte(data.String()), &meta)
			if err == nil && meta["type"] == "metadata" {
				fmt.Println("Received metadata:", meta)
				incomingFileMeta = meta
				receivedBytes = 0
				receivedChunks = make([]js.Value, 0)
				updateStatus(fmt.Sprintf("Receiving %s...", meta["name"]))
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

		totalSize := int(incomingFileMeta["size"].(float64))
		progress := int(float64(receivedBytes) / float64(totalSize) * 100)
		updateProgress(progress)

		if receivedBytes >= totalSize {
			fmt.Println("File received completely!")

			// Reassemble file
			array := js.Global().Get("Array").New()
			for _, chunk := range receivedChunks {
				array.Call("push", chunk)
			}

			blobType := "application/octet-stream"
			if incomingFileMeta["fileType"] != nil {
				blobType = incomingFileMeta["fileType"].(string)
			}

			blobOptions := map[string]interface{}{
				"type": blobType,
			}
			blob := js.Global().Get("Blob").New(array, blobOptions)

			url := js.Global().Get("URL").Call("createObjectURL", blob)
			a := document.Call("createElement", "a")
			a.Set("href", url)

			filename := "received_file"
			if incomingFileMeta["name"] != nil {
				filename = incomingFileMeta["name"].(string)
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
	}))
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

	meta := map[string]interface{}{
		"type":     "metadata",
		"name":     name,
		"fileType": fileType,
		"size":     size,
	}
	jsonMeta, _ := json.Marshal(meta)
	dc.Call("send", string(jsonMeta))

	document.Call("getElementById", "progress-container").Get("style").Set("display", "block")
	updateProgress(0)

	// Chunked reading and sending
	offset := 0
	reader := js.Global().Get("FileReader").New()

	var readNextChunk js.Func
	readNextChunk = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		// Check bufferedAmount to avoid overwhelming the channel
		if dc.Get("bufferedAmount").Int() > 16*1024*1024 { // 16MB buffer limit
			// Wait a bit
			window.Call("setTimeout", readNextChunk, 100)
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
		reader.Set("onload", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			res := reader.Get("result")
			dc.Call("send", res)

			offset = end
			progress := int(float64(offset) / float64(size) * 100)
			updateProgress(progress)

			// Schedule next chunk
			// Use setTimeout to yield to event loop
			window.Call("setTimeout", readNextChunk, 0)
			return nil
		}))
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
