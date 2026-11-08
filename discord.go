package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Discord webhook notifications
//
// Sends a Discord notification carrying the same information that's already
// written to the logs for each grab: either "clean" (no forbidden files
// found) or "malicious" (forbidden files found, listing which ones and why).
//
// Because these are sent from processGrab — which itself only runs after
// Sonarr/Radarr has already fired its own "Episode Grabbed" notification to
// Discord — this notification naturally lands as a follow-up in the same
// channel: Grabbed, then Clean/Blocked.
//
// Controlled by DISCORD_WEBHOOK_URL. If unset, notifications are silently
// skipped (no error, no retries) so this feature is fully opt-in.
// ---------------------------------------------------------------------------

type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
	Footer      *discordFooter      `json:"footer,omitempty"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type discordFooter struct {
	Text string `json:"text"`
}

type discordPayload struct {
	Username string         `json:"username,omitempty"`
	Embeds   []discordEmbed `json:"embeds"`
}

const (
	discordColorGreen = 0x2ECC71
	discordColorRed   = 0xE74C3C

	// Discord hard-limits each embed field value to 1024 characters.
	discordFieldLimit = 1000
)

// notifyDiscord posts a single embed to the configured webhook. Failures are
// only logged, never fatal — a Discord outage must never affect grab
// processing or file-blocking behavior.
func (app *App) notifyDiscord(embed discordEmbed) {
	webhookURL := strings.TrimSpace(app.config.DiscordWebhookURL)
	if webhookURL == "" {
		debugf("[Discord] DISCORD_WEBHOOK_URL not set, skipping notification")
		return
	}

	embed.Timestamp = time.Now().UTC().Format(time.RFC3339)
	if embed.Footer == nil {
		embed.Footer = &discordFooter{Text: "Release Guard"}
	}

	payload := discordPayload{
		Username: "Release Guard",
		Embeds:   []discordEmbed{embed},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		errorf("[Discord] Failed to marshal notification payload: %v", err)
		return
	}

	request, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		errorf("[Discord] Failed to build notification request: %v", err)
		return
	}
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		errorf("[Discord] Failed to send notification: %v", err)
		return
	}
	defer response.Body.Close()

	if response.StatusCode >= 300 {
		errorf("[Discord] Webhook returned HTTP %d", response.StatusCode)
		return
	}

	debugf("[Discord] Notification sent (HTTP %d)", response.StatusCode)
}

// notifyClean sends a "release is clean" notification mirroring the
// "[Name] CLEAN: <release>" log line.
func (app *App) notifyClean(arr ArrConfig, event GrabEvent, files []QBFile) {
	app.notifyDiscord(discordEmbed{
		Title:       fmt.Sprintf("✅ %s: Release Clean", arr.Name),
		Description: event.ReleaseTitle,
		Color:       discordColorGreen,
		Fields: []discordEmbedField{
			{Name: "Download ID", Value: event.DownloadID},
			{Name: "Files Scanned", Value: fmt.Sprintf("%d", len(files)), Inline: true},
		},
	})
}

// notifyBlocked sends a "release is malicious" notification mirroring the
// "FORBIDDEN FILE DETECTED" / "BLOCKING BECAUSE OF: <file>" log lines,
// including the full file listing so the notification carries the same
// detail as the logs.
func (app *App) notifyBlocked(arr ArrConfig, event GrabEvent, files []QBFile, forbiddenFiles []string) {
	forbidden := make(map[string]bool, len(forbiddenFiles))
	for _, name := range forbiddenFiles {
		forbidden[name] = true
	}

	fileLines := make([]string, 0, len(files))
	for _, file := range files {
		marker := "•"
		if forbidden[file.Name] {
			marker = "🚫"
		}
		fileLines = append(fileLines, fmt.Sprintf("%s %s (%d bytes)", marker, file.Name, file.Size))
	}

	blockedLines := make([]string, 0, len(forbiddenFiles))
	for _, name := range forbiddenFiles {
		blockedLines = append(blockedLines, fmt.Sprintf("• %s", name))
	}

	fields := []discordEmbedField{
		{Name: "Download ID", Value: event.DownloadID},
		{Name: "Blocked Because Of", Value: truncateForDiscord(strings.Join(blockedLines, "\n"))},
	}
	if len(fileLines) > 0 {
		fields = append(fields, discordEmbedField{
			Name:  fmt.Sprintf("Files Grabbed (%d)", len(files)),
			Value: truncateForDiscord(strings.Join(fileLines, "\n")),
		})
	}

	app.notifyDiscord(discordEmbed{
		Title:       fmt.Sprintf("🛑 %s: Malicious Release Blocked", arr.Name),
		Description: event.ReleaseTitle,
		Color:       discordColorRed,
		Fields:      fields,
	})
}

// notifyRemoved sends a follow-up notification confirming the malicious
// release was successfully removed from qBittorrent and blocklisted,
// mirroring the "SUCCESS: release removed from qBittorrent and blocklisted"
// log line.
func (app *App) notifyRemoved(arr ArrConfig, event GrabEvent, queueItem *QueueRecord) {
	app.notifyDiscord(discordEmbed{
		Title:       fmt.Sprintf("🗑️ %s: Removed & Blocklisted", arr.Name),
		Description: event.ReleaseTitle,
		Color:       discordColorRed,
		Fields: []discordEmbedField{
			{Name: "Queue Item", Value: fmt.Sprintf("%s (ID %d)", queueItem.Title, queueItem.ID)},
			{Name: "Download ID", Value: event.DownloadID},
		},
	})
}

// notifyActionFailed sends a notification when release-guard detected a
// malicious release but could not automatically remove/blocklist it,
// mirroring the corresponding "[Name] ... failed: <err>" error log line.
// Since nothing happened downstream, this is a "go check it yourself" alert.
func (app *App) notifyActionFailed(arr ArrConfig, event GrabEvent, reason string) {
	app.notifyDiscord(discordEmbed{
		Title:       fmt.Sprintf("⚠️ %s: Manual Action Needed", arr.Name),
		Description: event.ReleaseTitle,
		Color:       discordColorRed,
		Fields: []discordEmbedField{
			{Name: "Download ID", Value: event.DownloadID},
			{Name: "Reason", Value: truncateForDiscord(reason)},
		},
	})
}

// truncateForDiscord keeps a field value under Discord's per-field limit.
func truncateForDiscord(value string) string {
	if len(value) <= discordFieldLimit {
		return value
	}
	return value[:discordFieldLimit] + "\n…(truncated)"
}
