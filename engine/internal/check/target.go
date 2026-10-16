package check

import (
	"github.com/decisyon/dialga/engine/internal/config"
)

// RequestFor maps a configured target onto a check request.
func RequestFor(t config.Target) (Request, error) {
	req := Request{
		Profile:         t.AWSProfile,
		Region:          t.Region,
		Instance:        t.Instance,
		LocalPort:       t.Connect.LocalPort,
		SQLUser:         t.SQL.User,
		Bucket:          t.Bucket,
		ExpectedAccount: t.AccountID,
		Direct:          t.Connect.Mode == config.ModeDirect,
		Tunnel:          t.Connect.Mode == config.ModeTunnel,
		BridgeTag:       t.Connect.BridgeTag,
	}
	return req, req.Validate()
}
