# Windows MSI Packaging (WiX)

This directory contains WiX Toolset assets for building a Windows MSI installer.

Current behavior:

- Installs `ollama-gateway.exe` into `Program Files\\Ollama Gateway`
- Installs and starts a Windows service named `OllamaGateway`
- Runs `installer-bootstrap.exe` at install time to generate:
  - `C:\\ProgramData\\Ollama Gateway\\config.yaml`
  - `C:\\ProgramData\\Ollama Gateway\\bootstrap-admin.txt`
- Uses backend defaults unless MSI properties are overridden:
  - `BACKEND_NAME=local`
  - `BACKEND_URL=http://127.0.0.1:11434`

## Local Build

1. Install WiX Toolset v3.11 (`candle.exe`, `light.exe`).
2. Build MSI from repo root:

```powershell
./packaging/scripts/build-packages.ps1
```

Artifact output:

- `bin/packages/ollama-gateway_<version>_windows_<arch>.msi`

## Overriding Backend Inputs

You can set initial backend values at install time with public MSI properties:

```powershell
msiexec /i .\ollama-gateway_1.2.3_windows_amd64.msi BACKEND_NAME=prod BACKEND_URL=https://ollama.example.com
```

## Notes

- This is the first implementation slice. UI prompts for backend name and URL are planned next.
- The generated bootstrap file contains the one-time admin token. Store it securely.
