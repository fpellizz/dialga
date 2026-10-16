package check

import (
	"github.com/decisyon/dialga/engine/internal/config"
)

// RequestFor maps a configured target onto a check request.
func RequestFor(t config.Target) (Request, error) {
	if t.Connect.Mode == config.ModeTunnel {
		return Request{}, config.ErrTunnelNotYet
	}
	req := Request{
		Profile:         t.AWSProfile,
		Region:          t.Region,
		Instance:        t.Instance,
		LocalPort:       t.Connect.LocalPort,
		SQLUser:         t.SQL.User,
		Bucket:          t.Bucket,
		ExpectedAccount: t.AccountID,
		Direct:          t.Connect.Mode == config.ModeDirect,
	}
	return req, req.Validate()
}
