package handler

import (
	"fmt"
	"html/template"
	"net/http"
	"runtime"
)

// HomePageData holds data for the home page
type HomePageData struct {
	Healthy           bool
	HealthyCount      int
	MinUploadServers  int
	TotalServers      int
	TotalUploads      int64
	TotalDownloads    int64
	TotalMirrors      int64
	TotalDeletes      int64
	TotalLists        int64
	ServerStats       []ServerStat
	ServerAddress     string
	MemoryMB          float64
	MaxMemoryMB       float64
	MemoryHealthy     bool
	Goroutines        int
	MaxGoroutines     int
	GoroutinesHealthy bool
}

// ServerStat holds statistics for a single server
type ServerStat struct {
	URL                 string
	Healthy             bool
	ConsecutiveFailures int
	UploadsSuccess      int64
	UploadsFailure      int64
	Downloads           int64
	MirrorsSuccess      int64
	MirrorsFailure      int64
	DeletesSuccess      int64
	DeletesFailure      int64
	ListsSuccess        int64
	ListsFailure        int64
}

const homepageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Blossom Espelhator Tabajara - Status</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
            color: #333;
        }
        .container {
            max-width: 1200px;
            margin: 0 auto;
        }
        .header {
            background: white;
            border-radius: 10px;
            padding: 30px;
            margin-bottom: 20px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }
        .header h1 {
            color: #667eea;
            margin-bottom: 10px;
        }
        .status-badge {
            display: inline-block;
            padding: 8px 16px;
            border-radius: 20px;
            font-weight: bold;
            margin-top: 10px;
        }
        .status-healthy {
            background: #10b981;
            color: white;
        }
        .status-unhealthy {
            background: #ef4444;
            color: white;
        }
        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 20px;
            margin-bottom: 20px;
        }
        .stat-card {
            background: white;
            border-radius: 10px;
            padding: 20px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }
        .stat-card h3 {
            color: #667eea;
            font-size: 14px;
            text-transform: uppercase;
            margin-bottom: 10px;
        }
        .stat-value {
            font-size: 32px;
            font-weight: bold;
            color: #333;
        }
        .servers-section {
            background: white;
            border-radius: 10px;
            padding: 30px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }
        .servers-section h2 {
            color: #667eea;
            margin-bottom: 20px;
        }
        .server-card {
            border: 1px solid #e5e7eb;
            border-radius: 8px;
            padding: 20px;
            margin-bottom: 15px;
        }
        .server-card:last-child {
            margin-bottom: 0;
        }
        .server-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 15px;
        }
        .server-url {
            font-weight: bold;
            color: #333;
            word-break: break-all;
        }
        .server-health {
            padding: 4px 12px;
            border-radius: 12px;
            font-size: 12px;
            font-weight: bold;
        }
        .health-healthy {
            background: #d1fae5;
            color: #065f46;
        }
        .health-unhealthy {
            background: #fee2e2;
            color: #991b1b;
        }
        .server-stats {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
            gap: 15px;
            margin-top: 15px;
        }
        .server-stat-item {
            text-align: center;
        }
        .server-stat-label {
            font-size: 12px;
            color: #6b7280;
            margin-bottom: 5px;
        }
        .server-stat-value {
            font-size: 20px;
            font-weight: bold;
            color: #333;
        }
        .server-stat-success {
            color: #10b981;
        }
        .server-stat-failure {
            color: #ef4444;
        }
        .footer {
            text-align: center;
            margin-top: 30px;
            color: white;
            opacity: 0.8;
        }
        .docs-section {
            background: white;
            border-radius: 10px;
            padding: 30px;
            margin-top: 20px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }
        .docs-section h2 {
            color: #667eea;
            margin-bottom: 20px;
        }
        .docs-section h3 {
            color: #667eea;
            margin-top: 25px;
            margin-bottom: 15px;
        }
        .docs-section p {
            color: #4b5563;
            line-height: 1.6;
            margin-bottom: 15px;
        }
        .docs-section ul {
            margin-left: 20px;
            margin-bottom: 15px;
        }
        .docs-section li {
            color: #4b5563;
            margin-bottom: 8px;
            line-height: 1.6;
        }
        .docs-section code {
            background: #f3f4f6;
            padding: 2px 6px;
            border-radius: 4px;
            font-family: 'Courier New', monospace;
            color: #667eea;
            font-size: 0.9em;
        }
        .docs-section pre {
            background: #1f2937;
            color: #f3f4f6;
            padding: 15px;
            border-radius: 8px;
            overflow-x: auto;
            margin: 15px 0;
        }
        .docs-section pre code {
            background: transparent;
            color: #f3f4f6;
            padding: 0;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🌺 Blossom Espelhator Tabajara</h1>
            <p>Status Dashboard</p>
            <span class="status-badge {{if .Healthy}}status-healthy{{else}}status-unhealthy{{end}}">
                {{if .Healthy}}✓ Healthy{{else}}✗ Unhealthy{{end}}
            </span>
            <p style="margin-top: 15px; color: #6b7280;">
                {{.HealthyCount}} / {{.TotalServers}} servers healthy (minimum {{.MinUploadServers}} required)
            </p>
            <div style="margin-top: 20px; padding-top: 20px; border-top: 1px solid #e5e7eb;">
                <div style="display: flex; gap: 20px; flex-wrap: wrap;">
                    <div>
                        <strong>Memory:</strong> 
                        <span style="color: {{if .MemoryHealthy}}#10b981{{else}}#ef4444{{end}};">
                            {{printf "%.2f MB" .MemoryMB}} / {{printf "%.0f MB" .MaxMemoryMB}}
                            {{if .MemoryHealthy}}✓{{else}}✗{{end}}
                        </span>
                    </div>
                    <div>
                        <strong>Goroutines:</strong> 
                        <span style="color: {{if .GoroutinesHealthy}}#10b981{{else}}#ef4444{{end}};">
                            {{.Goroutines}} / {{.MaxGoroutines}}
                            {{if .GoroutinesHealthy}}✓{{else}}✗{{end}}
                        </span>
                    </div>
                </div>
            </div>
        </div>

        <div class="stats-grid">
            <div class="stat-card">
                <h3>Total Uploads</h3>
                <div class="stat-value">{{.TotalUploads}}</div>
            </div>
            <div class="stat-card">
                <h3>Total Downloads</h3>
                <div class="stat-value">{{.TotalDownloads}}</div>
            </div>
            <div class="stat-card">
                <h3>Total Mirrors</h3>
                <div class="stat-value">{{.TotalMirrors}}</div>
            </div>
            <div class="stat-card">
                <h3>Total Deletes</h3>
                <div class="stat-value">{{.TotalDeletes}}</div>
            </div>
            <div class="stat-card">
                <h3>Total Lists</h3>
                <div class="stat-value">{{.TotalLists}}</div>
            </div>
        </div>

        <div class="servers-section">
            <h2>Upstream Server Status</h2>
            {{range .ServerStats}}
            <div class="server-card">
                <div class="server-header">
                    <div class="server-url">{{.URL}}</div>
                    <span class="server-health {{if .Healthy}}health-healthy{{else}}health-unhealthy{{end}}">
                        {{if .Healthy}}✓ Healthy{{else}}✗ Unhealthy ({{.ConsecutiveFailures}} failures){{end}}
                    </span>
                </div>
                <div class="server-stats">
                    <div class="server-stat-item">
                        <div class="server-stat-label">Uploads</div>
                        <div class="server-stat-value">
                            <span class="server-stat-success">{{.UploadsSuccess}}</span> / 
                            <span class="server-stat-failure">{{.UploadsFailure}}</span>
                        </div>
                    </div>
                    <div class="server-stat-item">
                        <div class="server-stat-label">Downloads</div>
                        <div class="server-stat-value">{{.Downloads}}</div>
                    </div>
                    <div class="server-stat-item">
                        <div class="server-stat-label">Mirrors</div>
                        <div class="server-stat-value">
                            <span class="server-stat-success">{{.MirrorsSuccess}}</span> / 
                            <span class="server-stat-failure">{{.MirrorsFailure}}</span>
                        </div>
                    </div>
                    <div class="server-stat-item">
                        <div class="server-stat-label">Deletes</div>
                        <div class="server-stat-value">
                            <span class="server-stat-success">{{.DeletesSuccess}}</span> / 
                            <span class="server-stat-failure">{{.DeletesFailure}}</span>
                        </div>
                    </div>
                    <div class="server-stat-item">
                        <div class="server-stat-label">Lists</div>
                        <div class="server-stat-value">
                            <span class="server-stat-success">{{.ListsSuccess}}</span> / 
                            <span class="server-stat-failure">{{.ListsFailure}}</span>
                        </div>
                    </div>
                </div>
            </div>
            {{end}}
        </div>

        <div class="docs-section">
            <h2>📖 Usage</h2>
            
            <h3>Configure in Nostr Clients</h3>
            <p>To use this Blossom proxy server, configure your Nostr client to use this server address for media uploads:</p>
            <pre><code>{{.ServerAddress}}</code></pre>
            <p>Most Nostr clients allow you to configure a custom Blossom server in their settings. Look for:</p>
            <ul>
                <li><strong>Media/Blob Storage Settings</strong> - Set the Blossom server URL</li>
                <li><strong>File Upload Configuration</strong> - Specify this server for media uploads</li>
                <li><strong>Blossom Server URL</strong> - Enter the server address above</li>
            </ul>
            <p>When you upload files through your Nostr client:</p>
            <ul>
                <li>Files are automatically forwarded to multiple upstream Blossom servers</li>
                <li>The proxy ensures redundancy by uploading to at least {{.MinUploadServers}} servers</li>
                <li>SHA256 hashes are used for blob addressing</li>
                <li>Downloads automatically redirect to the best available upstream server</li>
            </ul>

            <h3>Access Files</h3>
            <p>Uploaded files can be accessed via:</p>
            <ul>
                <li><strong>Blossom URL:</strong> <code>{{.ServerAddress}}/&lt;sha256&gt;.&lt;ext&gt;</code> - Download endpoint (redirects to upstream server)</li>
                <li><strong>Direct URL:</strong> Use the URLs returned in upload responses from upstream servers</li>
            </ul>

            <h3>🔗 API Endpoints</h3>
            <ul>
                <li><strong>GET /</strong> - This home page</li>
                <li><strong>GET /health</strong> - Health check endpoint (returns JSON)</li>
                <li><strong>GET /stats</strong> - Statistics endpoint (returns JSON with detailed stats)</li>
                <li><strong>PUT /upload</strong> - Upload a file (Blossom protocol - forwards to upstream servers)</li>
                <li><strong>PUT /mirror</strong> - Mirror a blob (BUD-04 - forwards to upstream servers)</li>
                <li><strong>HEAD /upload</strong> - Upload preflight check (BUD-06 - checks upstream servers)</li>
                <li><strong>GET /list/&lt;pubkey&gt;</strong> - List files for a pubkey (Blossom protocol - merges results from all upstream servers)</li>
                <li><strong>GET /&lt;sha256&gt;.&lt;ext&gt;</strong> - Get file (redirects to upstream server)</li>
                <li><strong>HEAD /&lt;sha256&gt;.&lt;ext&gt;</strong> - Check file existence (proxies HEAD request)</li>
                <li><strong>DELETE /&lt;sha256&gt;</strong> - Delete file (forwards to all upstream servers that have it)</li>
            </ul>

            <h3>📋 About This Proxy</h3>
            <p>This is a Blossom protocol proxy server that forwards requests to multiple upstream Blossom servers. It provides:</p>
            <ul>
                <li><strong>Redundancy:</strong> Uploads are forwarded to multiple servers for backup</li>
                <li><strong>Load Distribution:</strong> Downloads are distributed across healthy upstream servers</li>
                <li><strong>Health Monitoring:</strong> Tracks server health and marks unhealthy servers after consecutive failures</li>
                <li><strong>Statistics:</strong> Aggregates statistics from all upstream servers</li>
                <li><strong>Unified API:</strong> Single endpoint for multiple upstream Blossom servers</li>
            </ul>
        </div>

        <div class="footer">
            <p>Blossom Espelhator Tabajara | <a href="/health" style="color: white;">Health API</a> | <a href="/stats" style="color: white;">Stats API</a></p>
        </div>
    </div>
</body>
</html>
`

// HandleHome handles GET / requests and serves the home page
func (h *BlossomHandler) HandleHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get health status
	healthyCount := h.stats.GetHealthyCount()
	minUploadServers := h.config.Server.MinUploadServers
	allStats := h.stats.GetAll()
	totalServers := len(allStats)

	// Get system metrics
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	memoryBytes := int64(m.Alloc)
	goroutines := runtime.NumGoroutine()

	// Check if system metrics are healthy
	memoryHealthy := memoryBytes < h.config.Server.MaxMemoryBytes
	goroutinesHealthy := goroutines < h.config.Server.MaxGoroutines
	serversHealthy := healthyCount >= minUploadServers

	// System is healthy if all checks pass
	isHealthy := memoryHealthy && goroutinesHealthy && serversHealthy

	// Get server address from request
	serverAddress := "http://" + r.Host
	if r.TLS != nil {
		serverAddress = "https://" + r.Host
	}

	// Calculate totals
	var totalUploads, totalDownloads, totalMirrors, totalDeletes, totalLists int64
	serverStats := make([]ServerStat, 0, len(allStats))

	for url, stats := range allStats {
		totalUploads += stats.UploadsSuccess + stats.UploadsFailure
		totalDownloads += stats.Downloads
		totalMirrors += stats.MirrorsSuccess + stats.MirrorsFailure
		totalDeletes += stats.DeletesSuccess + stats.DeletesFailure
		totalLists += stats.ListsSuccess + stats.ListsFailure

		serverStats = append(serverStats, ServerStat{
			URL:                 url,
			Healthy:             stats.IsHealthy,
			ConsecutiveFailures: stats.ConsecutiveFailures,
			UploadsSuccess:      stats.UploadsSuccess,
			UploadsFailure:      stats.UploadsFailure,
			Downloads:           stats.Downloads,
			MirrorsSuccess:      stats.MirrorsSuccess,
			MirrorsFailure:      stats.MirrorsFailure,
			DeletesSuccess:      stats.DeletesSuccess,
			DeletesFailure:      stats.DeletesFailure,
			ListsSuccess:        stats.ListsSuccess,
			ListsFailure:        stats.ListsFailure,
		})
	}

	data := HomePageData{
		Healthy:           isHealthy,
		HealthyCount:      healthyCount,
		MinUploadServers:  minUploadServers,
		TotalServers:      totalServers,
		TotalUploads:      totalUploads,
		TotalDownloads:    totalDownloads,
		TotalMirrors:      totalMirrors,
		TotalDeletes:      totalDeletes,
		TotalLists:        totalLists,
		ServerStats:       serverStats,
		ServerAddress:     serverAddress,
		MemoryMB:          float64(memoryBytes) / 1048576.0,
		MaxMemoryMB:       float64(h.config.Server.MaxMemoryBytes) / 1048576.0,
		MemoryHealthy:     memoryHealthy,
		Goroutines:        goroutines,
		MaxGoroutines:     h.config.Server.MaxGoroutines,
		GoroutinesHealthy: goroutinesHealthy,
	}

	tmpl, err := template.New("homepage").Parse(homepageHTML)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to parse template: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("Failed to execute template: %v", err), http.StatusInternalServerError)
		return
	}
}
