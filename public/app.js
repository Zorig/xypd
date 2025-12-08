(function () {
    'use strict';

    function track(action, label, value) {
        if (typeof window.trackEvent === 'function') {
            window.trackEvent(action, 'xypd', label, value);
        }
    }

    const go = new Go();
    WebAssembly.instantiateStreaming(fetch("main.wasm"), go.importObject)
        .then((result) => {
            go.run(result.instance);
            initializeApp();
            track('app_loaded', 'wasm_init');
        })
        .catch((err) => {
            console.error("Failed to load WASM:", err);
            track('error', 'wasm_init_failed');
            alert("Failed to initialize application. Please refresh the page.");
        });

    function initializeApp() {
        function showView(viewId) {
            document.querySelectorAll('.view').forEach(el => el.style.display = 'none');
            document.getElementById(viewId).style.display = 'block';
        }

        function startSending() {
            showView('sender-view');
            track('session_started', 'sender');
            if (typeof createSession === 'function') {
                createSession();
            }
        }

        function startReceiving() {
            showView('receiver-view');
            track('session_started', 'receiver');
        }

        function copyToClipboard(elementId) {
            const text = document.getElementById(elementId).innerText;
            navigator.clipboard.writeText(text).then(() => {
                track('room_id_copied', text);
                const btn = document.getElementById('btn-copy');
                const originalText = btn.innerText;
                btn.innerText = '✓';
                setTimeout(() => {
                    btn.innerText = originalText;
                }, 1500);
            }).catch(err => {
                console.error('Failed to copy: ', err);
                alert("Failed to copy to clipboard");
            });
        }

        function handleFileSelect(files) {
            if (files.length > 0) {
                const file = files[0];
                const fileNameDisplay = document.getElementById('selected-file-name');
                const sendBtn = document.getElementById('send-btn');
                const dropText = document.getElementById('drop-text');

                fileNameDisplay.innerText = "Selected: " + file.name;
                fileNameDisplay.style.display = 'block';
                sendBtn.style.display = 'block';
                dropText.style.display = 'none';

                track('file_selected', file.type || 'unknown', Math.round(file.size / 1024));
            }
        }

        const btnSend = document.getElementById('btn-send');
        if (btnSend) {
            btnSend.addEventListener('click', startSending);
        }

        const btnReceive = document.getElementById('btn-receive');
        if (btnReceive) {
            btnReceive.addEventListener('click', startReceiving);
        }

        const btnCopy = document.getElementById('btn-copy');
        if (btnCopy) {
            btnCopy.addEventListener('click', () => copyToClipboard('generated-room-id'));
        }

        const joinForm = document.getElementById('join-form');
        if (joinForm) {
            joinForm.addEventListener('submit', (e) => {
                e.preventDefault();
                const roomId = document.getElementById('roomIdInput').value;
                track('room_joined', roomId);
                if (typeof connectToRoom === 'function') {
                    connectToRoom(roomId);
                }
            });
        }

        const btnBack = document.getElementById('btn-back');
        if (btnBack) {
            btnBack.addEventListener('click', () => showView('landing-view'));
        }

        const dropZone = document.getElementById('drop-zone');
        const fileInput = document.getElementById('fileInput');

        if (dropZone && fileInput) {
            dropZone.addEventListener('click', () => fileInput.click());

            dropZone.addEventListener('dragover', (e) => {
                e.preventDefault();
                dropZone.classList.add('drag-over');
            });

            dropZone.addEventListener('dragleave', () => {
                dropZone.classList.remove('drag-over');
            });

            dropZone.addEventListener('drop', (e) => {
                e.preventDefault();
                dropZone.classList.remove('drag-over');
                if (e.dataTransfer.files.length) {
                    fileInput.files = e.dataTransfer.files;
                    handleFileSelect(e.dataTransfer.files);
                }
            });

            fileInput.addEventListener('change', () => {
                handleFileSelect(fileInput.files);
            });
        }

        const sendBtn = document.getElementById('send-btn');
        if (sendBtn) {
            sendBtn.addEventListener('click', () => {
                track('transfer_started', 'send');
                if (typeof sendFile === 'function') {
                    sendFile();
                }
            });
        }

        // Listen for transfer completion (from WASM)
        // The WASM code updates the status element, we can observe it
        const statusObserver = new MutationObserver((mutations) => {
            mutations.forEach((mutation) => {
                const text = mutation.target.innerText;
                if (text.includes('Sent') && text.includes('!')) {
                    track('transfer_completed', 'send');
                } else if (text.includes('Received') && text.includes('!')) {
                    track('transfer_completed', 'receive');
                } else if (text.includes('failed') || text.includes('Failed')) {
                    track('transfer_failed', text);
                }
            });
        });

        const statusElement = document.getElementById('status');
        if (statusElement) {
            statusObserver.observe(statusElement, { childList: true, characterData: true, subtree: true });
        }
    }
})();
