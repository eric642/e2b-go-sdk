package e2b

import (
	"context"
	"encoding/json"
	"time"

	"github.com/eric642/e2b-go-sdk/internal/envdapi"
)

// EnvdOOMKill records a process killed by the guest kernel OOM killer, as
// reported by envd GET /metrics.
type EnvdOOMKill struct {
	Process string
	Seq     int64
}

// EnvdMetrics holds live guest resource usage metrics returned directly by the
// in-sandbox envd daemon (GET /metrics). Unlike control-plane Sandbox.GetMetrics
// (which returns historical time-series samples), GetEnvdMetrics queries envd
// directly for an immediate snapshot including CPU hotplug state and OOM kills.
type EnvdMetrics struct {
	TimestampUnix     int64
	Timestamp         time.Time
	CPUCount          int32
	CPUPossible       int32
	CPUTarget         int32
	CPUTargetAttempts int32
	CPUWritePendingMs int64
	CPUUsedPct        float32
	MemTotal          int64
	MemUsed           int64
	MemCache          int64
	DiskUsed          int64
	DiskTotal         int64
	// OOMKills contains up to the latest 32 guest OOM kills since boot, or nil
	// when omitted by envd.
	OOMKills []EnvdOOMKill
}

// GetEnvs returns the default environment variables configured inside the
// sandbox's envd daemon.
//
// Endpoint: GET /envs (envd)
// Envd: >= v0.1.0
func (s *Sandbox) GetEnvs(ctx context.Context, opts ...CommandRequestOptions) (map[string]string, error) {
	o := firstCommandReqOpts(opts)
	cli := s.envdClients()
	if cli == nil || cli.API == nil {
		return nil, newSandboxError("envd client is not initialized", nil)
	}
	callCtx, cancel := withEnvdCallOpts(ctx, s, o.User, unaryEnvdTimeout(s, o.RequestTimeout))
	defer cancel()

	resp, err := cli.API.GetEnvs(callCtx)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, mapHTTPErr(resp, s.ID)
	}
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, newSandboxError("decode envd envs response", err)
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

// GetEnvdMetrics fetches an immediate guest resource usage snapshot directly
// from the in-sandbox envd daemon (including CPU hotplug state and recent OOM
// kills). Requires envd >= v0.1.5; returns *TemplateError on older builds.
//
// Endpoint: GET /metrics (envd)
// Envd: >= v0.1.5
func (s *Sandbox) GetEnvdMetrics(ctx context.Context, opts ...CommandRequestOptions) (*EnvdMetrics, error) {
	if err := s.requireEnvdVersion(envdMetrics, "envd metrics"); err != nil {
		return nil, err
	}
	o := firstCommandReqOpts(opts)
	cli := s.envdClients()
	if cli == nil || cli.API == nil {
		return nil, newSandboxError("envd client is not initialized", nil)
	}
	callCtx, cancel := withEnvdCallOpts(ctx, s, o.User, unaryEnvdTimeout(s, o.RequestTimeout))
	defer cancel()

	resp, err := cli.API.GetMetrics(callCtx)
	if err != nil {
		return nil, mapHTTPOrCtx(err)
	}
	defer func() {
		drainBody(resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return nil, mapHTTPErr(resp, s.ID)
	}
	var raw envdapi.Metrics
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, newSandboxError("decode envd metrics response", err)
	}
	out := &EnvdMetrics{}
	if raw.Ts != nil {
		out.TimestampUnix = *raw.Ts
		out.Timestamp = time.Unix(*raw.Ts, 0).UTC()
	}
	if raw.CpuCount != nil {
		out.CPUCount = int32(*raw.CpuCount)
	}
	if raw.CpuPossible != nil {
		out.CPUPossible = int32(*raw.CpuPossible)
	}
	if raw.CpuTarget != nil {
		out.CPUTarget = int32(*raw.CpuTarget)
	}
	if raw.CpuTargetAttempts != nil {
		out.CPUTargetAttempts = int32(*raw.CpuTargetAttempts)
	}
	if raw.CpuWritePendingMs != nil {
		out.CPUWritePendingMs = *raw.CpuWritePendingMs
	}
	if raw.CpuUsedPct != nil {
		out.CPUUsedPct = *raw.CpuUsedPct
	}
	if raw.MemTotal != nil {
		out.MemTotal = int64(*raw.MemTotal)
	}
	if raw.MemUsed != nil {
		out.MemUsed = int64(*raw.MemUsed)
	}
	if raw.MemCache != nil {
		out.MemCache = int64(*raw.MemCache)
	}
	if raw.DiskUsed != nil {
		out.DiskUsed = int64(*raw.DiskUsed)
	}
	if raw.DiskTotal != nil {
		out.DiskTotal = int64(*raw.DiskTotal)
	}
	if raw.OomKills != nil {
		out.OOMKills = make([]EnvdOOMKill, 0, len(*raw.OomKills))
		for _, k := range *raw.OomKills {
			out.OOMKills = append(out.OOMKills, EnvdOOMKill{
				Process: k.Process,
				Seq:     k.Seq,
			})
		}
	}
	return out, nil
}
