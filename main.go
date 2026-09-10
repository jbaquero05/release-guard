package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Leveled logging
//
// Controlled by LOG_LEVEL (DEBUG, INFO, WARN, ERROR). Defaults to DEBUG so
// that connection/auth problems are visible out of the box; turn it down to
// INFO once things are stable.
// ---------------------------------------------------------------------------

type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

var currentLogLevel = LevelDebug

func parseLogLevel(value string) LogLevel {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DEBUG":
		return LevelDebug
	case "INFO":
		return LevelInfo
	case "WARN", "WARNING":
		return LevelWarn
	case "ERROR":
		return LevelError
	default:
		return LevelDebug
	}
}

func logAt(level LogLevel, tag string, format string, args ...interface{}) {
	if level < currentLogLevel {
		return
	}
	log.Printf(tag+" "+format, args...)
}

func debugf(format string, args ...interface{}) { logAt(LevelDebug, "[DEBUG]", format, args...) }
func infof(format string, args ...interface{})  { logAt(LevelInfo, "[INFO] ", format, args...) }
func warnf(format string, args ...interface{})  { logAt(LevelWarn, "[WARN] ", format, args...) }
func errorf(format string, args ...interface{}) { logAt(LevelError, "[ERROR]", format, args...) }

// maskSecret shows just enough of a secret to confirm it's set/changed
// between deploys, without leaking it into logs.
func maskSecret(value string) string {
	if value == "" {
		return "(empty)"
	}
	if len(value) <= 4 {
		return "****"
	}
	return value[:2] + strings.Repeat("*", len(value)-4) + value[len(value)-2:]
}

// ---------------------------------------------------------------------------

type Config struct {
	SonarrURL    string
	SonarrAPIKey string

	RadarrURL    string
	RadarrAPIKey string

	QBittorrentURL      string
	QBittorrentUsername string
	QBittorrentPassword string

	MetadataTimeout      time.Duration
	MetadataPollInterval time.Duration

	QueueTimeout      time.Duration
	QueuePollInterval time.Duration

	ForbiddenExtensionsFile string

	StartupCheckRetries       int
	StartupCheckRetryInterval time.Duration
}

type ArrConfig struct {
	Name   string
	URL    string
	APIKey string
}

type GrabEvent struct {
	EventType      string `json:"eventType"`
	DownloadID     string `json:"downloadId"`
	ReleaseTitle   string `json:"releaseTitle"`
	DownloadClient string `json:"downloadClient"`
}

type QueueResponse struct {
	Records []QueueRecord `json:"records"`
}

type QueueRecord struct {
	ID         int64  `json:"id"`
	DownloadID string `json:"downloadId"`
	Title      string `json:"title"`
	Status     string `json:"status"`
}

type QBFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type App struct {
	config Config

	forbiddenExtensions map[string]struct{}

	qbClient *http.Client

	qbAuthMutex     sync.Mutex
	qbAuthenticated bool
}

func main() {
	currentLogLevel = parseLogLevel(getEnv("LOG_LEVEL", "INFO"))

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	infof("Release Guard starting up (log level=%s)", strings.ToUpper(getEnv("LOG_LEVEL", "DEBUG")))

	config := loadConfig()
	logResolvedConfig(config)

	forbiddenExtensions, err := loadForbiddenExtensions(
		config.ForbiddenExtensionsFile,
	)

	if err != nil {
		log.Fatalf(
			"Failed to load forbidden extensions: %v",
			err,
		)
	}

	jar, err := cookiejar.New(nil)

	if err != nil {
		log.Fatalf(
			"Failed to create HTTP cookie jar: %v",
			err,
		)
	}

	app := &App{
		config:              config,
		forbiddenExtensions: forbiddenExtensions,

		qbClient: &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
		},
	}

	infof(
		"Loaded %d forbidden extensions",
		len(forbiddenExtensions),
	)

	for extension := range forbiddenExtensions {
		debugf(
			"Forbidden extension: *.%s",
			extension,
		)
	}

	http.HandleFunc(
		"/health",
		app.healthHandler,
	)

	http.HandleFunc(
		"/webhook/sonarr",
		app.webhookHandler(
			ArrConfig{
				Name:   "Sonarr",
				URL:    config.SonarrURL,
				APIKey: config.SonarrAPIKey,
			},
		),
	)

	http.HandleFunc(
		"/webhook/radarr",
		app.webhookHandler(
			ArrConfig{
				Name:   "Radarr",
				URL:    config.RadarrURL,
				APIKey: config.RadarrAPIKey,
			},
		),
	)

	// Run connectivity/auth checks against Sonarr, Radarr, and qBittorrent
	// in the background. This is intentionally non-blocking: docker
	// compose doesn't guarantee those services are already up when this
	// container starts, so failures here retry for a while before being
	// logged as a hard failure, and the HTTP server (including /health)
	// comes up immediately regardless of the outcome.
	go app.runStartupChecks()

	address := getEnv(
		"LISTEN_ADDRESS",
		":8080",
	)

	infof(
		"Release Guard listening on %s",
		address,
	)

	log.Fatal(
		http.ListenAndServe(
			address,
			nil,
		),
	)
}

// logResolvedConfig dumps the fully-resolved configuration at startup
// (secrets masked) and flags the most common misconfigurations that lead to
// qBittorrent/Arr connection or authentication failures, so they show up
// immediately in the logs instead of being discovered via a stack of
// confusing HTTP errors later.
func logResolvedConfig(config Config) {
	infof("Resolved configuration:")
	infof("  SONARR_URL=%s", config.SonarrURL)
	infof("  SONARR_API_KEY=%s", maskSecret(config.SonarrAPIKey))
	infof("  RADARR_URL=%s", config.RadarrURL)
	infof("  RADARR_API_KEY=%s", maskSecret(config.RadarrAPIKey))
	infof("  QBITTORRENT_URL=%s", config.QBittorrentURL)
	infof("  QBITTORRENT_USERNAME=%s", config.QBittorrentUsername)
	infof("  QBITTORRENT_PASSWORD=%s", maskSecret(config.QBittorrentPassword))
	infof("  METADATA_TIMEOUT=%s METADATA_POLL_INTERVAL=%s", config.MetadataTimeout, config.MetadataPollInterval)
	infof("  QUEUE_TIMEOUT=%s QUEUE_POLL_INTERVAL=%s", config.QueueTimeout, config.QueuePollInterval)
	infof("  FORBIDDEN_EXTENSIONS_FILE=%s", config.ForbiddenExtensionsFile)
	infof("  STARTUP_CHECK_RETRIES=%d STARTUP_CHECK_RETRY_INTERVAL=%s", config.StartupCheckRetries, config.StartupCheckRetryInterval)

	if config.SonarrAPIKey == "" {
		warnf("SONARR_API_KEY is empty — requests to Sonarr will be rejected as unauthenticated")
	}

	if config.RadarrAPIKey == "" {
		warnf("RADARR_API_KEY is empty — requests to Radarr will be rejected as unauthenticated")
	}

	if config.QBittorrentPassword == "" {
		warnf("QBITTORRENT_PASSWORD is empty — qBittorrent login will fail")
	}

	if !strings.HasPrefix(config.QBittorrentURL, "http://") &&
		!strings.HasPrefix(config.QBittorrentURL, "https://") {

		warnf(
			"QBITTORRENT_URL=%q has no http:// or https:// scheme — the HTTP client and cookie jar cannot use it and requests will fail with an 'unsupported protocol scheme' error. Set it to e.g. http://qbittorrent:8080",
			config.QBittorrentURL,
		)
	}

	for _, arr := range []struct {
		name string
		raw  string
	}{
		{"SONARR_URL", config.SonarrURL},
		{"RADARR_URL", config.RadarrURL},
	} {
		if !strings.HasPrefix(arr.raw, "http://") && !strings.HasPrefix(arr.raw, "https://") {
			warnf(
				"%s=%q has no http:// or https:// scheme — requests will fail with an 'unsupported protocol scheme' error",
				arr.name,
				arr.raw,
			)
		}
	}
}

// runStartupChecks proactively verifies connectivity and authentication
// against every downstream service (qBittorrent, Sonarr, Radarr) as soon as
// the app starts, instead of waiting for the first real webhook to surface
// a problem. Each check retries for a while (StartupCheckRetries /
// StartupCheckRetryInterval) to tolerate services that are still starting
// up elsewhere in the compose stack, then logs a clear final verdict.
func (app *App) runStartupChecks() {

	infof("Running startup connectivity checks against qBittorrent, Sonarr, and Radarr...")

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		app.runPreflightCheck(
			"qBittorrent",
			app.checkQBittorrentConnectivity,
		)
	}()

	go func() {
		defer wg.Done()
		app.runPreflightCheck(
			"Sonarr",
			func() error {
				return app.checkArrConnectivity(
					ArrConfig{
						Name:   "Sonarr",
						URL:    app.config.SonarrURL,
						APIKey: app.config.SonarrAPIKey,
					},
				)
			},
		)
	}()

	go func() {
		defer wg.Done()
		app.runPreflightCheck(
			"Radarr",
			func() error {
				return app.checkArrConnectivity(
					ArrConfig{
						Name:   "Radarr",
						URL:    app.config.RadarrURL,
						APIKey: app.config.RadarrAPIKey,
					},
				)
			},
		)
	}()

	wg.Wait()

	infof("Startup connectivity checks complete")
}

// runPreflightCheck retries the given check up to StartupCheckRetries times,
// waiting StartupCheckRetryInterval between attempts, and logs a single
// clear OK/FAILED verdict line at the end so it's impossible to miss in the
// logs regardless of log level.
func (app *App) runPreflightCheck(
	name string,
	check func() error,
) {

	attempts := app.config.StartupCheckRetries

	if attempts < 1 {
		attempts = 1
	}

	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {

		start := time.Now()
		err := check()

		if err == nil {
			infof(
				"STARTUP CHECK OK: %s reachable and authenticated (attempt %d/%d, %s)",
				name,
				attempt,
				attempts,
				time.Since(start),
			)
			return
		}

		lastErr = err

		if attempt < attempts {
			warnf(
				"STARTUP CHECK: %s attempt %d/%d failed: %v (retrying in %s)",
				name,
				attempt,
				attempts,
				err,
				app.config.StartupCheckRetryInterval,
			)
			time.Sleep(app.config.StartupCheckRetryInterval)
		}
	}

	errorf(
		"STARTUP CHECK FAILED: %s unreachable or unauthenticated after %d attempts: %v",
		name,
		attempts,
		lastErr,
	)
}

// checkQBittorrentConnectivity forces a fresh login attempt (ignoring any
// cached session state) so the startup check reflects reality rather than a
// stale "already authenticated" flag.
func (app *App) checkQBittorrentConnectivity() error {

	app.qbAuthMutex.Lock()
	app.qbAuthenticated = false
	app.qbAuthMutex.Unlock()

	return app.qbLogin()
}

// checkArrConnectivity hits the Arr system status endpoint, which requires
// a valid API key but has no other side effects, making it a safe
// authentication probe.
func (app *App) checkArrConnectivity(arr ArrConfig) error {

	requestURL := arr.URL + "/api/v3/system/status"

	debugf(
		"[%s] Startup check: GET %s (API key=%s)",
		arr.Name,
		requestURL,
		maskSecret(arr.APIKey),
	)

	request, err := http.NewRequest(
		http.MethodGet,
		requestURL,
		nil,
	)

	if err != nil {
		return fmt.Errorf("building %s status request: %w", arr.Name, err)
	}

	request.Header.Set("X-Api-Key", arr.APIKey)

	client := &http.Client{Timeout: 10 * time.Second}

	response, err := client.Do(request)

	if err != nil {
		return fmt.Errorf(
			"connecting to %s at %s: %w",
			arr.Name,
			arr.URL,
			err,
		)
	}

	defer response.Body.Close()

	body, _ := io.ReadAll(response.Body)

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf(
			"%s rejected the request as unauthenticated (HTTP %d) — check %s_API_KEY matches Settings > General in %s: %s",
			arr.Name,
			response.StatusCode,
			strings.ToUpper(arr.Name),
			arr.Name,
			string(body),
		)
	}

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf(
			"%s returned HTTP %d: %s",
			arr.Name,
			response.StatusCode,
			string(body),
		)
	}

	return nil
}

func loadConfig() Config {
	return Config{
		SonarrURL: strings.TrimRight(
			getEnv(
				"SONARR_URL",
				"http://sonarr:8989",
			),
			"/",
		),

		SonarrAPIKey: getEnv(
			"SONARR_API_KEY",
			"",
		),

		RadarrURL: strings.TrimRight(
			getEnv(
				"RADARR_URL",
				"http://radarr:7878",
			),
			"/",
		),

		RadarrAPIKey: getEnv(
			"RADARR_API_KEY",
			"",
		),

		QBittorrentURL: strings.TrimRight(
			getEnv(
				"QBITTORRENT_URL",
				"http://qbittorrent:8080",
			),
			"/",
		),

		QBittorrentUsername: getEnv(
			"QBITTORRENT_USERNAME",
			"admin",
		),

		QBittorrentPassword: getEnv(
			"QBITTORRENT_PASSWORD",
			"",
		),

		MetadataTimeout: time.Duration(
			getEnvInt(
				"METADATA_TIMEOUT_SECONDS",
				90,
			),
		) * time.Second,

		MetadataPollInterval: time.Duration(
			getEnvInt(
				"METADATA_POLL_INTERVAL_SECONDS",
				2,
			),
		) * time.Second,

		QueueTimeout: time.Duration(
			getEnvInt(
				"QUEUE_TIMEOUT_SECONDS",
				30,
			),
		) * time.Second,

		QueuePollInterval: time.Duration(
			getEnvInt(
				"QUEUE_POLL_INTERVAL_SECONDS",
				2,
			),
		) * time.Second,

		ForbiddenExtensionsFile: getEnv(
			"FORBIDDEN_EXTENSIONS_FILE",
			"/config/forbidden_extensions.txt",
		),

		StartupCheckRetries: getEnvInt(
			"STARTUP_CHECK_RETRIES",
			5,
		),

		StartupCheckRetryInterval: time.Duration(
			getEnvInt(
				"STARTUP_CHECK_RETRY_INTERVAL_SECONDS",
				5,
			),
		) * time.Second,
	}
}

func loadForbiddenExtensions(
	filename string,
) (map[string]struct{}, error) {

	data, err := os.ReadFile(filename)

	if err != nil {
		return nil, err
	}

	result := make(map[string]struct{})

	for _, line := range strings.Split(
		string(data),
		"\n",
	) {

		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		if strings.HasPrefix(
			line,
			"#",
		) {
			continue
		}

		/*
			Your format:

				*.exe
				*.zipx
				*.scr

			We also accept:

				.exe
				exe
		*/

		line = strings.ToLower(line)

		line = strings.TrimPrefix(
			line,
			"*",
		)

		line = strings.TrimPrefix(
			line,
			".",
		)

		if line == "" {
			continue
		}

		result[line] = struct{}{}
	}

	return result, nil
}

func (app *App) healthHandler(
	writer http.ResponseWriter,
	request *http.Request,
) {

	writer.Header().Set(
		"Content-Type",
		"application/json",
	)

	writer.WriteHeader(
		http.StatusOK,
	)

	_, _ = writer.Write(
		[]byte(`{"status":"ok"}`),
	)
}

func (app *App) webhookHandler(
	arr ArrConfig,
) http.HandlerFunc {

	return func(
		writer http.ResponseWriter,
		request *http.Request,
	) {

		if request.Method != http.MethodPost {

			warnf(
				"[%s] Rejected %s request from %s (method not allowed)",
				arr.Name,
				request.Method,
				request.RemoteAddr,
			)

			http.Error(
				writer,
				"Method not allowed",
				http.StatusMethodNotAllowed,
			)

			return
		}

		body, err := io.ReadAll(
			http.MaxBytesReader(
				writer,
				request.Body,
				2*1024*1024,
			),
		)

		if err != nil {

			warnf(
				"[%s] Failed to read request body from %s: %v",
				arr.Name,
				request.RemoteAddr,
				err,
			)

			http.Error(
				writer,
				"Unable to read request",
				http.StatusBadRequest,
			)

			return
		}

		infof(
			"[%s] Webhook received from %s (%d bytes)",
			arr.Name,
			request.RemoteAddr,
			len(body),
		)

		debugf(
			"[%s] Raw payload: %s",
			arr.Name,
			string(body),
		)

		var event GrabEvent

		if err := json.Unmarshal(
			body,
			&event,
		); err != nil {

			warnf(
				"[%s] Invalid JSON: %v",
				arr.Name,
				err,
			)

			http.Error(
				writer,
				"Invalid JSON",
				http.StatusBadRequest,
			)

			return
		}

		/*
			Acknowledge immediately.

			The actual inspection happens asynchronously.
		*/

		writer.WriteHeader(
			http.StatusOK,
		)

		go app.processGrab(
			arr,
			event,
			body,
		)
	}
}

func (app *App) processGrab(
	arr ArrConfig,
	event GrabEvent,
	rawPayload []byte,
) {

	start := time.Now()

	debugf(
		"[%s] EventType=%q",
		arr.Name,
		event.EventType,
	)

	debugf(
		"[%s] Release=%q",
		arr.Name,
		event.ReleaseTitle,
	)

	debugf(
		"[%s] DownloadClient=%q",
		arr.Name,
		event.DownloadClient,
	)

	debugf(
		"[%s] DownloadID=%q",
		arr.Name,
		event.DownloadID,
	)

	if !strings.EqualFold(
		event.EventType,
		"Grab",
	) {

		debugf(
			"[%s] Ignoring non-Grab event (EventType=%q)",
			arr.Name,
			event.EventType,
		)

		return
	}

	downloadID := strings.TrimSpace(
		event.DownloadID,
	)

	/*
		If the typed struct didn't get the field,
		try the raw JSON explicitly.
	*/

	if downloadID == "" {

		debugf(
			"[%s] downloadId missing from decoded event, attempting raw JSON extraction",
			arr.Name,
		)

		downloadID = extractJSONString(
			rawPayload,
			"downloadId",
		)

		if downloadID != "" {
			debugf(
				"[%s] Recovered downloadId=%q from raw payload",
				arr.Name,
				downloadID,
			)
		}
	}

	if downloadID == "" {

		errorf(
			"[%s] Grab event contains no downloadId",
			arr.Name,
		)

		errorf(
			"[%s] Payload: %s",
			arr.Name,
			string(rawPayload),
		)

		return
	}

	if event.DownloadClient != "" &&
		!strings.Contains(
			strings.ToLower(event.DownloadClient),
			"qbit",
		) &&
		!strings.Contains(
			strings.ToLower(event.DownloadClient),
			"torrent",
		) {

		infof(
			"[%s] Download client does not appear to be qBittorrent: %q",
			arr.Name,
			event.DownloadClient,
		)

		/*
			Don't inspect/remove something from another
			download client.
		*/

		return
	}

	infof(
		"[%s] Waiting for qBittorrent metadata for %s",
		arr.Name,
		downloadID,
	)

	files, err := app.waitForTorrentFiles(
		downloadID,
	)

	if err != nil {

		errorf(
			"[%s] Retrieving torrent files for %s failed after %s: %v",
			arr.Name,
			downloadID,
			time.Since(start),
			err,
		)

		return
	}

	infof(
		"[%s] qBittorrent returned %d files for %s",
		arr.Name,
		len(files),
		downloadID,
	)

	for _, file := range files {

		debugf(
			"[%s] Torrent file: %s (%d bytes)",
			arr.Name,
			file.Name,
			file.Size,
		)
	}

	var forbiddenFiles []string

	for _, file := range files {

		if app.isForbidden(
			file.Name,
		) {

			forbiddenFiles = append(
				forbiddenFiles,
				file.Name,
			)
		}
	}

	if len(forbiddenFiles) == 0 {

		infof(
			"[%s] CLEAN: %s",
			arr.Name,
			event.ReleaseTitle,
		)

		return
	}

	warnf(
		"[%s] ========================================",
		arr.Name,
	)

	warnf(
		"[%s] FORBIDDEN FILE DETECTED",
		arr.Name,
	)

	for _, filename := range forbiddenFiles {

		warnf(
			"[%s] BLOCKING BECAUSE OF: %s",
			arr.Name,
			filename,
		)
	}

	warnf(
		"[%s] ========================================",
		arr.Name,
	)

	queueItem, err := app.waitForQueueItem(
		arr,
		downloadID,
	)

	if err != nil {

		errorf(
			"[%s] Locating Arr queue item for %s failed: %v",
			arr.Name,
			downloadID,
			err,
		)

		return
	}

	infof(
		"[%s] Queue item found: ID=%d Title=%q",
		arr.Name,
		queueItem.ID,
		queueItem.Title,
	)

	if err := app.removeAndBlocklist(
		arr,
		queueItem.ID,
	); err != nil {

		errorf(
			"[%s] Removing/blocklisting queue item %d failed: %v",
			arr.Name,
			queueItem.ID,
			err,
		)

		return
	}

	infof(
		"[%s] SUCCESS: release removed from qBittorrent and blocklisted (took %s)",
		arr.Name,
		time.Since(start),
	)
}

func (app *App) isForbidden(
	filename string,
) bool {

	extension := strings.ToLower(
		filepath.Ext(filename),
	)

	extension = strings.TrimPrefix(
		extension,
		".",
	)

	if extension == "" {
		return false
	}

	_, exists := app.forbiddenExtensions[
		extension,
	]

	return exists
}

func (app *App) waitForTorrentFiles(
	hash string,
) ([]QBFile, error) {

	deadline := time.Now().Add(
		app.config.MetadataTimeout,
	)

	attempt := 0

	var lastErr error

	for {

		attempt++

		debugf(
			"Querying qBittorrent for files of %s (attempt %d)",
			hash,
			attempt,
		)

		files, err := app.getTorrentFiles(
			hash,
		)

		if err == nil && len(files) > 0 {
			return files, nil
		}

		if err != nil {
			lastErr = err

			warnf(
				"qBittorrent file query not ready for %s (attempt %d): %v",
				hash,
				attempt,
				err,
			)
		} else {
			debugf(
				"qBittorrent returned 0 files for %s (attempt %d), retrying",
				hash,
				attempt,
			)
		}

		if time.Now().After(
			deadline,
		) {

			if lastErr != nil {
				return nil, lastErr
			}

			return nil, fmt.Errorf(
				"metadata timeout: qBittorrent returned no files",
			)
		}

		time.Sleep(
			app.config.MetadataPollInterval,
		)
	}
}

// newQBRequest builds a request against the qBittorrent WebUI API with the
// headers it requires.
//
// qBittorrent's WebUI has CSRF protection that validates the Referer (and,
// depending on version, Origin) header against its own configured address.
// If those headers are absent or don't match, qBittorrent silently rejects
// the request with "403 Forbidden" — including the login request itself —
// no matter how correct the username/password are. This is the most common
// reason a script "can't authenticate" against qBittorrent even with valid
// credentials, so every request sets both headers here.
func (app *App) newQBRequest(
	method string,
	requestURL string,
	body io.Reader,
) (*http.Request, error) {

	request, err := http.NewRequest(
		method,
		requestURL,
		body,
	)

	if err != nil {
		return nil, err
	}

	request.Header.Set(
		"Referer",
		app.config.QBittorrentURL,
	)

	request.Header.Set(
		"Origin",
		app.config.QBittorrentURL,
	)

	return request, nil
}

func (app *App) qbLogin() error {

	app.qbAuthMutex.Lock()
	defer app.qbAuthMutex.Unlock()

	if app.qbAuthenticated {
		return nil
	}

	loginURL := app.config.QBittorrentURL + "/api/v2/auth/login"

	debugf(
		"Logging in to qBittorrent at %s as user %q",
		loginURL,
		app.config.QBittorrentUsername,
	)

	form := url.Values{}

	form.Set(
		"username",
		app.config.QBittorrentUsername,
	)

	form.Set(
		"password",
		app.config.QBittorrentPassword,
	)

	request, err := app.newQBRequest(
		http.MethodPost,
		loginURL,
		strings.NewReader(
			form.Encode(),
		),
	)

	if err != nil {
		return fmt.Errorf("building qBittorrent login request: %w", err)
	}

	request.Header.Set(
		"Content-Type",
		"application/x-www-form-urlencoded",
	)

	start := time.Now()

	response, err := app.qbClient.Do(
		request,
	)

	if err != nil {
		return fmt.Errorf(
			"connecting to qBittorrent at %s: %w",
			app.config.QBittorrentURL,
			err,
		)
	}

	defer response.Body.Close()

	body, _ := io.ReadAll(
		response.Body,
	)

	elapsed := time.Since(start)

	hasCookie := response.Header.Get("Set-Cookie") != ""

	debugf(
		"qBittorrent login responded HTTP %d in %s (Set-Cookie present=%t)",
		response.StatusCode,
		elapsed,
		hasCookie,
	)

	// qBittorrent 5.2.0 changed the WebAPI to send HTTP 204 No Content
	// (instead of 200 with body "Ok.") for any successful response that
	// has no data to return, and login is one such endpoint. Older
	// versions still return 200 with a body, so both are accepted here.
	// See qBittorrent release notes: "WEBAPI: Send 204 when WebAPI
	// response contains no data".
	switch response.StatusCode {

	case http.StatusNoContent:
		// Success on qBittorrent 5.2.0+: empty body is expected, so
		// there's no "Ok."/"Fails." text to check. A missing
		// Set-Cookie header here would be unusual for a real success,
		// so surface it as a warning without treating it as fatal.
		if !hasCookie {
			warnf(
				"qBittorrent login returned HTTP 204 (success) but no Set-Cookie header — session may not persist across requests",
			)
		}

	case http.StatusOK:
		if !strings.Contains(
			string(body),
			"Ok.",
		) {

			errorf(
				"qBittorrent login rejected despite HTTP 200: body=%s. This usually means the username/password in QBITTORRENT_USERNAME/QBITTORRENT_PASSWORD are wrong, or qBittorrent has temporarily banned this IP for too many failed attempts.",
				string(body),
			)

			return fmt.Errorf(
				"qBittorrent login rejected: %s",
				string(body),
			)
		}

	default:
		errorf(
			"qBittorrent login failed: HTTP %d, body=%s. If credentials look correct, check that QBITTORRENT_URL matches the address qBittorrent's WebUI is actually configured with, and that no IP-ban ('too many failed login attempts') is currently in effect.",
			response.StatusCode,
			string(body),
		)

		return fmt.Errorf(
			"qBittorrent login HTTP %d: %s",
			response.StatusCode,
			string(body),
		)
	}

	app.qbAuthenticated = true

	infof(
		"Authenticated with qBittorrent at %s in %s",
		app.config.QBittorrentURL,
		elapsed,
	)

	return nil
}

func (app *App) getTorrentFiles(
	hash string,
) ([]QBFile, error) {

	if err := app.qbLogin(); err != nil {
		return nil, err
	}

	requestURL :=
		app.config.QBittorrentURL +
			"/api/v2/torrents/files?hash=" +
			url.QueryEscape(hash)

	debugf(
		"Requesting qBittorrent torrent files: GET %s",
		requestURL,
	)

	request, err := app.newQBRequest(
		http.MethodGet,
		requestURL,
		nil,
	)

	if err != nil {
		return nil, fmt.Errorf("building qBittorrent files request: %w", err)
	}

	start := time.Now()

	response, err := app.qbClient.Do(
		request,
	)

	if err != nil {
		return nil, fmt.Errorf(
			"connecting to qBittorrent at %s: %w",
			app.config.QBittorrentURL,
			err,
		)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(
		response.Body,
	)

	if err != nil {
		return nil, err
	}

	debugf(
		"qBittorrent files query responded HTTP %d in %s (%d bytes)",
		response.StatusCode,
		time.Since(start),
		len(body),
	)

	if response.StatusCode ==
		http.StatusForbidden ||
		response.StatusCode ==
			http.StatusUnauthorized {

		app.qbAuthMutex.Lock()
		app.qbAuthenticated = false
		app.qbAuthMutex.Unlock()

		warnf(
			"qBittorrent session expired or was rejected (HTTP %d) for hash=%s, will re-authenticate on next attempt. Body=%s",
			response.StatusCode,
			hash,
			string(body),
		)

		return nil, fmt.Errorf(
			"qBittorrent session expired: HTTP %d",
			response.StatusCode,
		)
	}

	// qBittorrent 5.2.0+ sends 204 No Content (instead of 200 with an
	// empty JSON array) when there's no data to return — for this
	// endpoint that means "no files yet" (e.g. metadata still being
	// fetched), not an error. Treat it as zero files so the caller's
	// normal polling/retry logic handles it.
	if response.StatusCode == http.StatusNoContent {

		debugf(
			"qBittorrent returned HTTP 204 (no files yet) for hash=%s",
			hash,
		)

		return nil, nil
	}

	if response.StatusCode != http.StatusOK {

		errorf(
			"qBittorrent files API returned HTTP %d for hash=%s: %s",
			response.StatusCode,
			hash,
			string(body),
		)

		return nil, fmt.Errorf(
			"qBittorrent files API HTTP %d: %s",
			response.StatusCode,
			string(body),
		)
	}

	var files []QBFile

	if err := json.Unmarshal(
		body,
		&files,
	); err != nil {

		errorf(
			"Failed to parse qBittorrent files response for hash=%s: %v. Body=%s",
			hash,
			err,
			string(body),
		)

		return nil, fmt.Errorf(
			"invalid qBittorrent file response: %w",
			err,
		)
	}

	return files, nil
}

func (app *App) waitForQueueItem(
	arr ArrConfig,
	downloadID string,
) (*QueueRecord, error) {

	deadline := time.Now().Add(
		app.config.QueueTimeout,
	)

	attempt := 0

	var lastErr error

	for {

		attempt++

		item, err := app.findQueueItem(
			arr,
			downloadID,
		)

		if err == nil {
			return item, nil
		}

		lastErr = err

		debugf(
			"[%s] Queue item not available yet for %s (attempt %d): %v",
			arr.Name,
			downloadID,
			attempt,
			err,
		)

		if time.Now().After(
			deadline,
		) {
			break
		}

		time.Sleep(
			app.config.QueuePollInterval,
		)
	}

	return nil, fmt.Errorf(
		"queue item timeout: %w",
		lastErr,
	)
}

func (app *App) findQueueItem(
	arr ArrConfig,
	downloadID string,
) (*QueueRecord, error) {

	requestURL :=
		arr.URL +
			"/api/v3/queue?page=1&pageSize=1000"

	debugf(
		"[%s] Requesting Arr queue: GET %s (API key=%s)",
		arr.Name,
		requestURL,
		maskSecret(arr.APIKey),
	)

	request, err := http.NewRequest(
		http.MethodGet,
		requestURL,
		nil,
	)

	if err != nil {
		return nil, err
	}

	request.Header.Set(
		"X-Api-Key",
		arr.APIKey,
	)

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	start := time.Now()

	response, err := client.Do(
		request,
	)

	if err != nil {
		return nil, fmt.Errorf(
			"connecting to %s at %s: %w",
			arr.Name,
			arr.URL,
			err,
		)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(
		response.Body,
	)

	if err != nil {
		return nil, err
	}

	debugf(
		"[%s] Queue API responded HTTP %d in %s (%d bytes)",
		arr.Name,
		response.StatusCode,
		time.Since(start),
		len(body),
	)

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {

		errorf(
			"[%s] Queue API rejected the request as unauthenticated (HTTP %d). Check that %s_API_KEY matches the key shown in %s's Settings > General.",
			arr.Name,
			response.StatusCode,
			strings.ToUpper(arr.Name),
			arr.Name,
		)
	}

	if response.StatusCode != http.StatusOK {

		return nil, fmt.Errorf(
			"Arr queue API HTTP %d: %s",
			response.StatusCode,
			string(body),
		)
	}

	var queue QueueResponse

	if err := json.Unmarshal(
		body,
		&queue,
	); err != nil {

		return nil, fmt.Errorf(
			"invalid Arr queue response: %w",
			err,
		)
	}

	debugf(
		"[%s] Arr queue contains %d record(s)",
		arr.Name,
		len(queue.Records),
	)

	for i := range queue.Records {

		record := &queue.Records[i]

		if strings.EqualFold(
			strings.TrimSpace(record.DownloadID),
			strings.TrimSpace(downloadID),
		) {

			return record, nil
		}
	}

	return nil, fmt.Errorf(
		"downloadId %q not found in Arr queue",
		downloadID,
	)
}

func (app *App) removeAndBlocklist(
	arr ArrConfig,
	queueID int64,
) error {

	params := url.Values{}

	params.Set(
		"removeFromClient",
		"true",
	)

	params.Set(
		"blocklist",
		"true",
	)

	params.Set(
		"skipRedownload",
		"false",
	)

	requestURL := fmt.Sprintf(
		"%s/api/v3/queue/%d?%s",
		arr.URL,
		queueID,
		params.Encode(),
	)

	debugf(
		"[%s] Requesting Arr queue removal: DELETE %s (API key=%s)",
		arr.Name,
		requestURL,
		maskSecret(arr.APIKey),
	)

	request, err := http.NewRequest(
		http.MethodDelete,
		requestURL,
		nil,
	)

	if err != nil {
		return err
	}

	request.Header.Set(
		"X-Api-Key",
		arr.APIKey,
	)

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	start := time.Now()

	response, err := client.Do(
		request,
	)

	if err != nil {
		return fmt.Errorf(
			"connecting to %s at %s: %w",
			arr.Name,
			arr.URL,
			err,
		)
	}

	defer response.Body.Close()

	body, _ := io.ReadAll(
		response.Body,
	)

	debugf(
		"[%s] Queue removal responded HTTP %d in %s",
		arr.Name,
		response.StatusCode,
		time.Since(start),
	)

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			errorf(
				"[%s] Queue removal rejected as unauthenticated (HTTP %d). Check %s_API_KEY.",
				arr.Name,
				response.StatusCode,
				strings.ToUpper(arr.Name),
			)
		}

		return fmt.Errorf(
			"Arr removal API HTTP %d: %s",
			response.StatusCode,
			string(body),
		)
	}

	return nil
}

func extractJSONString(
	data []byte,
	field string,
) string {

	var payload map[string]interface{}

	if err := json.Unmarshal(
		data,
		&payload,
	); err != nil {
		return ""
	}

	value, ok := payload[field]

	if !ok {
		return ""
	}

	result, ok := value.(string)

	if !ok {
		return ""
	}

	return result
}

func getEnv(
	key string,
	defaultValue string,
) string {

	value := strings.TrimSpace(
		os.Getenv(key),
	)

	if value == "" {
		return defaultValue
	}

	return value
}

func getEnvInt(
	key string,
	defaultValue int,
) int {

	value := getEnv(
		key,
		"",
	)

	if value == "" {
		return defaultValue
	}

	parsed, err := strconv.Atoi(
		value,
	)

	if err != nil {
		warnf(
			"%s=%q is not a valid integer, falling back to default %d",
			key,
			value,
			defaultValue,
		)
		return defaultValue
	}

	return parsed
}

func getEnvBool(
	key string,
	defaultValue bool,
) bool {

	value := strings.ToLower(
		getEnv(
			key,
			"",
		),
	)

	switch value {

	case "true", "1", "yes", "on":
		return true

	case "false", "0", "no", "off":
		return false

	default:
		return defaultValue
	}
}