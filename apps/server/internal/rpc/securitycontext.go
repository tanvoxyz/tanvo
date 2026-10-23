package rpc

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/clientip"
	"github.com/tanvoxyz/tanvo/apps/server/internal/useragent"
	"github.com/tanvoxyz/tanvo/libs/auth"
)

// SecurityContext returns the request context of security log events, for
// [auth.NewRepoSecurityLogger]: the user of a user principal and, except
// for API keys, the client device (lago CurrentContext.device_info, set
// by graphql_controller.rb:35 from Utils::DeviceInfo.parse). ips resolves
// the client address.
func SecurityContext(ips *clientip.Resolver) func(context.Context) auth.SecurityContext {
	return func(ctx context.Context) auth.SecurityContext {
		var out auth.SecurityContext
		p, ok := Principal(ctx)
		if ok && p.Kind == auth.PrincipalAPIKey {
			return out
		}
		if ok && p.User != nil {
			out.UserID = p.User.ID
		}
		info, ok := connect.CallInfoForServerContext(ctx)
		if !ok {
			return out
		}
		h := info.RequestHeader()
		ua := h.Get("User-Agent")
		if ua == "" {
			return out
		}
		d := useragent.Parse(ua)
		var ip string
		if ips != nil {
			ip = ips.Resolve(info.PeerAddr, h)
		}
		out.DeviceInfo = map[string]any{
			"user_agent": ua, "ip_address": ip, "browser": d.Browser, "os": d.OS, "device_type": d.DeviceType,
		}
		return out
	}
}
