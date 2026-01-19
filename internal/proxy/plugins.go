package proxy

import (
	"context"
	"errors"
	"net/http"

	"modern_reverse_proxy/internal/plugin"
	"modern_reverse_proxy/internal/plugin/proto"
	"modern_reverse_proxy/internal/policy"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pluginTracking struct {
	bypassed       bool
	bypassReason   string
	failureMode    string
	shortCircuit   bool
	mutationDenied bool
}

func (p *pluginTracking) markBypass(reason string, mode plugin.FailureMode) {
	if p == nil {
		return
	}
	p.bypassed = true
	if p.bypassReason == "" {
		p.bypassReason = reason
	}
	if p.failureMode == "" {
		p.failureMode = string(mode)
	}
}

func (p *pluginTracking) markFailureMode(mode plugin.FailureMode) {
	if p == nil {
		return
	}
	if p.failureMode == "" {
		p.failureMode = string(mode)
	}
}

func (p *pluginTracking) markShortCircuit() {
	if p == nil {
		return
	}
	p.shortCircuit = true
}

func (p *pluginTracking) markMutationDenied() {
	if p == nil {
		return
	}
	p.mutationDenied = true
}

func (h *Handler) applyRequestPlugins(recorder *ResponseRecorder, r *http.Request, route policy.Route, requestID string, tracking *pluginTracking) bool {
	plugins := route.Policy.Plugins
	if !plugins.Enabled || len(plugins.Filters) == 0 {
		return false
	}
	for _, filter := range plugins.Filters {
		if r.Context().Err() != nil {
			return false
		}

		if h.PluginRegistry == nil {
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "request", "error") {
				return true
			}
			continue
		}

		filterKey := plugin.FilterKey(filter.Name, filter.Addr) + ":request"
		breaker := h.PluginRegistry.GetBreaker(filterKey, filter.Breaker)
		if breaker != nil {
			_, allowed := breaker.Allow()
			if !allowed {
				if h.Metrics != nil {
					h.Metrics.RecordPluginCall(filter.Name, "request", "breaker_bypass")
					h.Metrics.RecordPluginBypass(filter.Name, "breaker_open")
				}
				tracking.markBypass("breaker_open", filter.FailureMode)
				if filter.FailureMode == plugin.FailureModeFailClose {
					tracking.markFailureMode(filter.FailureMode)
					if h.Metrics != nil {
						h.Metrics.RecordPluginFailClosed(filter.Name)
					}
					WriteProxyError(recorder, requestID, http.StatusServiceUnavailable, "plugin_unavailable", "plugin unavailable")
					return true
				}
				continue
			}
		}

		client, err := h.PluginRegistry.GetClient(filter.Addr)
		if err != nil {
			if breaker != nil {
				breaker.Report(false)
			}
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "request", "error") {
				return true
			}
			continue
		}

		ctx, cancel := context.WithTimeout(r.Context(), filter.RequestTimeout)
		resp, err := client.ApplyRequest(ctx, &pluginpb.ApplyRequestRequest{
			RequestId:   requestID,
			RouteId:     route.ID,
			Method:      r.Method,
			Host:        r.Host,
			Path:        r.URL.Path,
			Headers:     plugin.HeadersToMap(r.Header),
			BodyPreview: nil,
		})
		cancel()
		if err != nil {
			if breaker != nil {
				breaker.Report(false)
			}
			result := pluginErrorResult(err)
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "request", result) {
				return true
			}
			continue
		}

		if resp == nil {
			if breaker != nil {
				breaker.Report(false)
			}
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "request", "error") {
				return true
			}
			continue
		}

		switch resp.GetAction() {
		case pluginpb.ApplyRequestResponse_CONTINUE:
			if h.Metrics != nil {
				h.Metrics.RecordPluginCall(filter.Name, "request", "success")
			}
			if breaker != nil {
				breaker.Report(true)
			}
			if plugin.ApplyHeaderMutations(r.Header, resp.GetMutatedHeaders()) {
				tracking.markMutationDenied()
			}
			continue
		case pluginpb.ApplyRequestResponse_RESPOND:
			status := int(resp.GetResponseStatus())
			if status <= 0 {
				if breaker != nil {
					breaker.Report(false)
				}
				if h.handlePluginFailure(recorder, requestID, filter, tracking, "request", "error") {
					return true
				}
				continue
			}
			if h.Metrics != nil {
				h.Metrics.RecordPluginCall(filter.Name, "request", "success")
			}
			if breaker != nil {
				breaker.Report(true)
			}
			if plugin.ApplyHeaderMutations(recorder.Header(), resp.GetResponseHeaders()) {
				tracking.markMutationDenied()
			}
			recorder.Header().Set(RequestIDHeader, requestID)
			recorder.WriteHeader(status)
			if r.Method != http.MethodHead {
				_, _ = recorder.Write(resp.GetResponseBody())
			}
			tracking.markShortCircuit()
			if h.Metrics != nil {
				h.Metrics.RecordPluginShortCircuit(filter.Name)
			}
			return true
		default:
			if breaker != nil {
				breaker.Report(false)
			}
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "request", "error") {
				return true
			}
			continue
		}
	}
	return false
}

func (h *Handler) applyResponsePlugins(recorder *ResponseRecorder, r *http.Request, resp *http.Response, route policy.Route, requestID string, tracking *pluginTracking) bool {
	plugins := route.Policy.Plugins
	if !plugins.Enabled || len(plugins.Filters) == 0 {
		return false
	}
	if resp == nil {
		return false
	}
	for _, filter := range plugins.Filters {
		if r.Context().Err() != nil {
			return false
		}
		if h.PluginRegistry == nil {
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "response", "error") {
				if resp.Body != nil {
					resp.Body.Close()
				}
				return true
			}
			continue
		}
		filterKey := plugin.FilterKey(filter.Name, filter.Addr) + ":response"
		breaker := h.PluginRegistry.GetBreaker(filterKey, filter.Breaker)
		if breaker != nil {
			_, allowed := breaker.Allow()
			if !allowed {
				if h.Metrics != nil {
					h.Metrics.RecordPluginCall(filter.Name, "response", "breaker_bypass")
					h.Metrics.RecordPluginBypass(filter.Name, "breaker_open")
				}
				tracking.markBypass("breaker_open", filter.FailureMode)
				if filter.FailureMode == plugin.FailureModeFailClose {
					tracking.markFailureMode(filter.FailureMode)
					if h.Metrics != nil {
						h.Metrics.RecordPluginFailClosed(filter.Name)
					}
					WriteProxyError(recorder, requestID, http.StatusServiceUnavailable, "plugin_unavailable", "plugin unavailable")
					if resp.Body != nil {
						resp.Body.Close()
					}
					return true
				}
				continue
			}
		}

		client, err := h.PluginRegistry.GetClient(filter.Addr)
		if err != nil {
			if breaker != nil {
				breaker.Report(false)
			}
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "response", "error") {
				if resp.Body != nil {
					resp.Body.Close()
				}
				return true
			}
			continue
		}

		ctx, cancel := context.WithTimeout(r.Context(), filter.ResponseTimeout)
		pluginResp, err := client.ApplyResponse(ctx, &pluginpb.ApplyResponseRequest{
			RequestId:       requestID,
			RouteId:         route.ID,
			UpstreamStatus:  int32(resp.StatusCode),
			UpstreamHeaders: plugin.HeadersToMap(resp.Header),
		})
		cancel()
		if err != nil {
			if breaker != nil {
				breaker.Report(false)
			}
			result := pluginErrorResult(err)
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "response", result) {
				if resp.Body != nil {
					resp.Body.Close()
				}
				return true
			}
			continue
		}

		if pluginResp == nil {
			if breaker != nil {
				breaker.Report(false)
			}
			if h.handlePluginFailure(recorder, requestID, filter, tracking, "response", "error") {
				if resp.Body != nil {
					resp.Body.Close()
				}
				return true
			}
			continue
		}

		if h.Metrics != nil {
			h.Metrics.RecordPluginCall(filter.Name, "response", "success")
		}
		if breaker != nil {
			breaker.Report(true)
		}
		if plugin.ApplyHeaderMutations(resp.Header, pluginResp.GetMutatedHeaders()) {
			tracking.markMutationDenied()
		}
	}
	return false
}

func (h *Handler) handlePluginFailure(recorder *ResponseRecorder, requestID string, filter plugin.Filter, tracking *pluginTracking, phase string, result string) bool {
	if h.Metrics != nil {
		h.Metrics.RecordPluginCall(filter.Name, phase, result)
		if filter.FailureMode == plugin.FailureModeFailOpen {
			h.Metrics.RecordPluginBypass(filter.Name, result)
		}
	}
	if filter.FailureMode == plugin.FailureModeFailOpen {
		tracking.markBypass(result, filter.FailureMode)
	} else {
		tracking.markFailureMode(filter.FailureMode)
	}
	if filter.FailureMode == plugin.FailureModeFailClose {
		if h.Metrics != nil {
			h.Metrics.RecordPluginFailClosed(filter.Name)
		}
		category := "plugin_unavailable"
		if result == "timeout" {
			category = "plugin_timeout"
		}
		WriteProxyError(recorder, requestID, http.StatusServiceUnavailable, category, "plugin unavailable")
		return true
	}
	return false
}

func pluginErrorResult(err error) string {
	if err == nil {
		return "error"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if status.Code(err) == codes.DeadlineExceeded {
		return "timeout"
	}
	return "error"
}
