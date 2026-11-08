# Release Guard

A lightweight, event-driven download validator for **Sonarr**, **Radarr**, and **qBittorrent**.

Release Guard listens for `Grab` events from Sonarr and Radarr, inspects the files inside the corresponding qBittorrent torrent, and automatically rejects releases containing forbidden file extensions.

When a forbidden file is detected, Release Guard tells the originating Arr application to:

* Remove the torrent from qBittorrent
* Blocklist the release
* Prevent the release from being immediately re-downloaded

## How It Works

```text
Sonarr / Radarr
      |
      | Grab webhook
      v
Release Guard
      |
      | Query torrent files
      v
qBittorrent
      |
      | Forbidden extension?
      |
     YES
      |
      v
Sonarr / Radarr
      |
      +--> Remove from download client
      |
      +--> Blocklist release
      |
      +--> Prevent re-download
```

Release Guard does not need direct access to the media filesystem. It operates entirely through the Arr and qBittorrent APIs.

## Features

* Supports **Sonarr**
* Supports **Radarr**
* Event-driven webhook architecture
* qBittorrent torrent file inspection
* Case-insensitive extension matching
* Glob-style forbidden extension configuration
* Automatic Arr blocklisting
* Automatic torrent removal
* Retry handling for torrents whose metadata is not immediately available
* Runs as a small Docker container
* Written in Go
* No database required

## Requirements

* Docker
* Docker Compose
* Sonarr and/or Radarr
* qBittorrent
* Network connectivity between Release Guard and the Arr/qBittorrent containers

## Docker Image

A pre-built Docker image is available on Docker Hub, so you can run Release Guard without building the image yourself.

```bash
docker pull frodosynthesis12/release-guard:latest
```

The image is available at:

**Docker Hub:** https://hub.docker.com/r/frodosynthesis12/release-guard

For reproducible deployments, you can pin a specific version instead of using `latest`:

```bash
docker pull frodosynthesis12/release-guard:0.1.0
```

### Using the Docker Hub Image

Replace the `build` section of the Release Guard Compose service with the image:

```yaml
release-guard:
  image: frodosynthesis12/release-guard:latest

  container_name: release-guard

  env_file:
    - ./config/release-guard/.env

  environment:
    - TZ=YOUR_TZ
    - FORBIDDEN_EXTENSIONS_FILE=/config/forbidden_extensions.txt
    - LISTEN_ADDRESS=:8080

  volumes:
    - ./config/release-guard/forbidden_extensions.txt:/config/forbidden_extensions.txt:ro

  ports:
    - "8088:8080"

  restart: unless-stopped
```

Then pull and start the container:

```bash
docker compose pull release-guard
docker compose up -d release-guard
```

## Configuration

Release Guard is configured through environment variables.

Example:

```env
SONARR_URL=http://sonarr:8989
SONARR_API_KEY=your_sonarr_api_key

RADARR_URL=http://radarr:7878
RADARR_API_KEY=your_radarr_api_key

QBITTORRENT_URL=http://qbittorrent:8080
QBITTORRENT_USERNAME=your_qbittorrent_username
QBITTORRENT_PASSWORD=your_qbittorrent_password

DRY_RUN=false
```

Keep `.env` out of version control.

The forbidden extension list is supplied through:

```env
FORBIDDEN_EXTENSIONS_FILE=/config/forbidden_extensions.txt
```

## Forbidden Extensions

Extensions are specified one per line using the `*.extension` format.

Example:

```text
*.zipx
*.zip
*.rar
*.7z
*.exe
*.iso
```

Matching is case-insensitive, so:

```text
*.ZIPX
```

will also match:

```text
example.zipx
example.ZIPX
example.ZipX
```

Blank lines are ignored.

Lines beginning with `#` can be used for comments:

```text
# Archive formats
*.zip
*.rar
*.7z

# Executables
*.exe
*.msi
```

## Docker Compose

Example service:

```yaml
release-guard:
  image: frodosynthesis12/release-guard:latest

  container_name: release-guard

  env_file:
    - ./config/release-guard/.env

  environment:
    - TZ=YOUR_TZ
    - FORBIDDEN_EXTENSIONS_FILE=/config/forbidden_extensions.txt
    - LISTEN_ADDRESS=:8080

  volumes:
    - ./config/release-guard/forbidden_extensions.txt:/config/forbidden_extensions.txt:ro

  ports:
    - "8088:8080"

  restart: unless-stopped
```

The container listens on port `8080`, while the host exposes it on port `8088`.

qBittorrent can therefore continue using host port `8080`:

```text
Host :8080  -> qBittorrent :8080
Host :8088  -> Release Guard :8080
```

Inside the Docker network, Release Guard communicates with qBittorrent using:

```text
http://qbittorrent:8080
```

## Arr Webhooks

Create a webhook in both Sonarr and Radarr.

### Sonarr

Set the webhook URL to:

```text
http://release-guard:8080/webhook/sonarr
```

Enable the **Grab** event.

### Radarr

Set the webhook URL to:

```text
http://release-guard:8080/webhook/radarr
```

Enable the **Grab** event.

The webhook provides Release Guard with the download ID/hash needed to locate the torrent in qBittorrent.

## Project Structure

```text
release-guard/
├── Dockerfile
├── go.mod
├── main.go
├── forbidden_extensions.txt
├── .env
└── .gitignore
```

The `.env` file should never be committed.

## Building

If you want to build the image yourself:

```bash
docker compose build release-guard
```

Or directly with Docker:

```bash
docker build -t frodosynthesis12/release-guard:latest .
```

Start the service:

```bash
docker compose up -d release-guard
```

View logs:

```bash
docker compose logs -f release-guard
```

## Testing

A useful way to test the guard is to add a deliberately forbidden extension:

```text
*.zipx
```

Then grab a release containing a `.zipx` file through Sonarr or Radarr.

Release Guard should:

1. Receive the Grab webhook.
2. Identify the qBittorrent torrent.
3. Retrieve its file list.
4. Detect the `.zipx` file.
5. Remove the torrent from qBittorrent.
6. Blocklist the release in Sonarr/Radarr.

A release without forbidden files should be left untouched.


## Security

API credentials should be supplied through environment variables rather than committed to the repository.

Do not commit:

```text
.env
```

The forbidden extension configuration can safely be committed because it does not contain credentials.

If an API key has been exposed publicly, revoke it and generate a new one.
