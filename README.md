# lightning-node-connect

Placeholder repository for everything that is needed for Lightning Node Connect.

## Run the example WASM application

### Run LiT

1. Compile and run `LiT` from the [`mailbox` branch of the closed beta
   repo](https://gitlab.com/lightning-labs/lightning-terminal).
2. Navigate to the "Lightning Node Connect" page and copy the password of the
   default session for later use.

### Compile and run the example

1. In this repo, run `make wasm` to compile the example client to WASM.
2. Run `make example-server` to start the example HTTP server.
3. Visit [http://localhost:8080](http://localhost:8080) to see the example
   client in action. It is recommended to open the browser console (F12) to see
   the console logs.
