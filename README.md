# Release Guard

A lightweight, event-driven download validator and automated source cleanup service for **Sonarr**, **Radarr**, and **qBittorrent**.

Release Guard listens for `Grab`, `Import`, and `Upgrade` events from Sonarr and Radarr. It inspects torrent contents, automatically rejects releases containing forbidden file extensions, and can remove original downloaded files after Arr successfully imports an independent copy into the media library.

Release Guard also supports retroactive cleanup: a one-time startup scan that identifies torrents whose files have already been imported into Radarr or Sonarr.

## How It Works

### Download Validation

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

### Imported Source Cleanup

```text
Sonarr / Radarr
      |
      | Import / Upgrade webhook
      v
Release Guard
      |
      | Locate imported media files
      v
qBittorrent
      |
      | Match source and destination files
      | Verify file size and SHA-256
      | Check for hardlinks
      |
      | All checks passed?
      |
     YES
      |
      v
Release Guard
      |
      +--> Delete verified source files
      |
      +--> Remove torrent from qBittorrent
           and delete remaining downloaded files
```

### Retroactive Cleanup

```text
Release Guard Startup
      |
      | Connectivity checks succeed
      v
Radarr / Sonarr Library
      |
      | Retrieve existing media files
      v
qBittorrent
      |
      | Filter by Arr category
      | Match files by size and SHA-256
      |
      | Unambiguous independent copy?
      |
     YES
      |
      v
Release Guard
      |
      +--> Apply normal cleanup verification
      |
      +--> Delete verified source files
      |
      +--> Remove torrent from qBittorrent
```

## Features

* Supports **Sonarr**
* Supports **Radarr**
* Event-driven webhook architecture
* qBittorrent torrent file inspection
* Case-insensitive extension matching
* Glob-style forbidden extension configuration
* Automatic Arr blocklisting
* Automatic torrent removal for forbidden releases
* Retry handling for torrents whose metadata is not immediately available
* Imported source cleanup after successful Arr imports
* File size and SHA-256 verification before source deletion
* Hardlink detection to prevent deleting files still shared with the media library
* Automatic qBittorrent torrent removal after successful source cleanup
* Deletes remaining downloaded files when removing a torrent
* One-time retroactive cleanup of existing Radarr and Sonarr library files
* Independent retroactive cleanup switches for Radarr and Sonarr
* qBittorrent category filtering during retroactive scans
* Dry-run mode for both normal and retroactive cleanup
* Runs as a small Docker container
* Written in Go
* No database required

## Requirements

* Docker
* Docker Compose
* Sonarr and/or Radarr
* qBittorrent
* Network connectivity between Release Guard and the Arr/qBittorrent containers
* Shared access to the download and media directories for imported source cleanup and retroactive scanning

## Docker Image

A pre-built Docker image is available on Docker Hub, so you can run Release Guard without building the image yourself.

```bash
docker pull frodosynthesis12/release-guard:latest
```

The image is available at:

**Docker Hub:** [https://hub.docker.com/r/frodosynthesis12/release-guard](https://hub.docker.com/r/frodosynthesis12/release-guard)

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

### Connection Settings

Example `.env`:

```env
SONARR_URL=http://sonarr:8989
SONARR_API_KEY=your_sonarr_api_key

RADARR_URL=http://radarr:7878
RADARR_API_KEY=your_radarr_api_key

QBITTORRENT_URL=http://qbittorrent:8080
QBITTORRENT_USERNAME=your_qbittorrent_username
QBITTORRENT_PASSWORD=your_qbittorrent_password
```

Keep `.env` out of version control.

### Cleanup Settings

Imported source cleanup and retroactive cleanup are controlled through the following environment variables:

```env
CLEANUP_ENABLED=true
CLEANUP_DRY_RUN=true

CLEANUP_DOWNLOAD_ROOT=/data/torrents
CLEANUP_MEDIA_ROOT=/data/media

RETROACTIVE_RADARR_CLEANUP_ENABLED=false
RETROACTIVE_SONARR_CLEANUP_ENABLED=false
```

| Variable | Description |
|---|---|
| `CLEANUP_ENABLED` | Enables or disables imported source cleanup. |
| `CLEANUP_DRY_RUN` | When `true`, performs checks and logs planned actions without deleting files or removing torrents. Applies to normal and retroactive cleanup. |
| `CLEANUP_DOWNLOAD_ROOT` | Root directory containing downloaded torrent data as seen inside the Release Guard container. |
| `CLEANUP_MEDIA_ROOT` | Root directory containing imported media files as seen inside the Release Guard container. |
| `RETROACTIVE_RADARR_CLEANUP_ENABLED` | Enables the one-time Radarr library scan at startup. |
| `RETROACTIVE_SONARR_CLEANUP_ENABLED` | Enables the one-time Sonarr library scan at startup. |

Both retroactive switches default to `false`.

You can enable either application independently or enable both.

For example, to run a Radarr-only dry run:

```env
CLEANUP_ENABLED=true
CLEANUP_DRY_RUN=true

RETROACTIVE_RADARR_CLEANUP_ENABLED=true
RETROACTIVE_SONARR_CLEANUP_ENABLED=false
```

This scans the Radarr library without scanning Sonarr.

### qBittorrent Categories

Retroactive cleanup uses qBittorrent categories to keep Radarr and Sonarr torrents separate.

| Application | Required qBittorrent category |
|---|---|
| Radarr | `radarr` |
| Sonarr | `tv-sonarr` |

Only torrents matching the corresponding category are inspected during each application's retroactive scan.

Category matching is case-insensitive and ignores surrounding whitespace.

Torrents belonging to other categories, including uncategorized torrents, are skipped.

### Forbidden Extension File

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

    - CLEANUP_ENABLED=true
    - CLEANUP_DRY_RUN=true

    - CLEANUP_DOWNLOAD_ROOT=/data/torrents
    - CLEANUP_MEDIA_ROOT=/data/media

    - RETROACTIVE_RADARR_CLEANUP_ENABLED=false
    - RETROACTIVE_SONARR_CLEANUP_ENABLED=false

  volumes:
    - ./config/release-guard/forbidden_extensions.txt:/config/forbidden_extensions.txt:ro
    - /path/to/downloads:/data/torrents:rw
    - /path/to/media:/data/media:rw

  ports:
    - "8088:8080"

  restart: unless-stopped
```

The download and media directories must be accessible to Release Guard using the same paths configured in `CLEANUP_DOWNLOAD_ROOT` and `CLEANUP_MEDIA_ROOT`.

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

Enable the following events:

* **Grab** — validates the torrent contents and rejects releases containing forbidden extensions.
* **Import** — verifies imported media files and performs source cleanup when eligible.
* **Upgrade** — performs the same cleanup checks after an upgraded file is imported.

### Radarr

Set the webhook URL to:

```text
http://release-guard:8080/webhook/radarr
```

Enable the following events:

* **Grab** — validates the torrent contents and rejects releases containing forbidden extensions.
* **Import** — verifies imported media files and performs source cleanup when eligible.
* **Upgrade** — performs the same cleanup checks after an upgraded file is imported.

The Grab webhook provides Release Guard with the download ID/hash needed to locate the torrent in qBittorrent.

Import and Upgrade webhooks provide the imported library file paths used during source verification.

## Imported Source Cleanup

When an Import or Upgrade event is received, Release Guard compares the imported library files with their corresponding files in qBittorrent.

Before deleting anything, it verifies:

1. The imported file is inside `CLEANUP_MEDIA_ROOT`.
2. The original source file is inside `CLEANUP_DOWNLOAD_ROOT`.
3. Both files are regular files and not symbolic links.
4. Both files have the same size.
5. Both files have matching SHA-256 checksums.
6. The source and destination are not hardlinks to the same underlying file.
7. The source match is unambiguous.

If all checks pass, Release Guard deletes the verified source files and then asks qBittorrent to remove the torrent with its downloaded files.

If verification fails, the source is left untouched.

**Important:** Removing a torrent with the delete-files option also deletes any remaining downloaded files belonging to that torrent, including files that Arr may not have imported. Review dry-run logs before enabling actual deletion.

## Retroactive Cleanup

Retroactive cleanup is intended for files that were imported before Release Guard was configured.

When enabled, it runs once at startup, after the connectivity checks against qBittorrent, Sonarr, and Radarr succeed.

The scan performs the following steps:

1. Retrieves existing movie files from Radarr when Radarr retroactive cleanup is enabled.
2. Retrieves existing episode files from Sonarr when Sonarr retroactive cleanup is enabled.
3. Retrieves the torrents currently registered in qBittorrent.
4. Filters torrents by the corresponding Arr category.
5. Compares existing library files against torrent files using file size and SHA-256 checksums.
6. Excludes hardlinks and ambiguous matches.
7. Passes eligible file/torrent combinations through the same cleanup verification logic used by normal Import events.

Radarr and Sonarr are scanned independently.

For example, to scan only Radarr:

```env
RETROACTIVE_RADARR_CLEANUP_ENABLED=true
RETROACTIVE_SONARR_CLEANUP_ENABLED=false
```

To scan only Sonarr:

```env
RETROACTIVE_RADARR_CLEANUP_ENABLED=false
RETROACTIVE_SONARR_CLEANUP_ENABLED=true
```

To scan both:

```env
RETROACTIVE_RADARR_CLEANUP_ENABLED=true
RETROACTIVE_SONARR_CLEANUP_ENABLED=true
```

The scan runs only once per application startup. Restarting Release Guard with a retroactive switch enabled triggers that scan again.

If startup connectivity checks fail, the retroactive scan is skipped.

## Dry Run and Testing

Always begin by enabling:

```env
CLEANUP_DRY_RUN=true
```

Dry-run mode performs the matching and verification checks and logs the actions that would be taken, but it does not delete source files or remove torrents from qBittorrent.

This applies to both normal webhook-driven cleanup and retroactive cleanup.

### Testing Download Validation

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
5. Ask the originating Arr application to remove the torrent from the download client and blocklist the release.

A release without forbidden files should be left untouched by the download validator.

### Testing Imported Source Cleanup

Import a release where Arr creates an independent copy instead of a hardlink.

Release Guard should:

1. Receive the Import or Upgrade webhook.
2. Identify the imported media files.
3. Locate the original files in qBittorrent.
4. Verify file size and SHA-256 checksums.
5. Confirm that the source and destination are not hardlinks.
6. Log the source files and torrent that would be removed.

Review the dry-run logs before disabling dry-run.

### Testing Retroactive Cleanup

To test Radarr only:

```env
CLEANUP_ENABLED=true
CLEANUP_DRY_RUN=true

RETROACTIVE_RADARR_CLEANUP_ENABLED=true
RETROACTIVE_SONARR_CLEANUP_ENABLED=false
```

Restart Release Guard:

```bash
docker compose up -d --force-recreate release-guard
docker compose logs -f release-guard
```

Inspect the logs to verify that:

* Radarr library files were retrieved successfully.
* Only torrents in the `radarr` category were inspected.
* Matching files passed the size and SHA-256 checks.
* Ambiguous matches and hardlinks were skipped.
* Planned source-file and torrent removals are correct.

Once the results have been reviewed, set:

```env
CLEANUP_DRY_RUN=false
```

Then recreate the container to apply the setting.

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

## Security

API credentials should be supplied through environment variables rather than committed to the repository.

Do not commit:

```text
.env
```

The forbidden extension configuration can safely be committed because it does not contain credentials.

If an API key has been exposed publicly, revoke it and generate a new one.