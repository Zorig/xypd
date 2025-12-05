# xypd

**xypd** is a secure, peer-to-peer file transfer application powered by **Go**, **WebAssembly (WASM)**, and **WebRTC**. It allows you to share files of any size directly between devices without storing them on a server.

## Features

-   **Peer-to-Peer**: Direct transfer between clients. No data is stored on the server.
-   **Secure**: End-to-end encryption via WebRTC.
-   **Unlimited File Size**: Smart chunking (16KB) allows transferring large files reliably.
-   **Simple & Fast**: No passwords required. Just share the 6-character Room ID.
-   **Modern UI**: Drag and drop support with real-time progress tracking.
-   **Go Everywhere**: Backend in Go, Frontend logic in Go (WASM).

## Tech Stack

-   **Server**: Go (Gorilla WebSocket) for signaling.
-   **Client**: Go (compiled to WASM) for WebRTC logic.
-   **Frontend**: HTML5, CSS3, JavaScript (glue code).
-   **Deployment**: Docker, Railway, Render.

## Local Development

1.  **Prerequisites**: Go 1.23+
2.  **Run**:
    ```bash
    make run
    ```
    Access at `http://localhost:8080`.

## Deployment

### Railway / Render
This project is ready for deployment on Railway or Render.
1.  Push to GitHub.
2.  Connect repository to Railway/Render.
3.  The `Dockerfile` handles the multi-stage build (Go server + WASM client).

## Security
-   **Room ID**: 6-character alphanumeric ID for easy but secure sharing.
-   **CSP**: Strict Content Security Policy to prevent XSS.
## Contributing
Feel free to contribute! Open an issue or submit a pull request if you have ideas for improvements or new features.

## License
This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
