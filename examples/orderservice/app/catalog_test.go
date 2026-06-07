package app_test

import (
	"context"
	"errors"
	"testing"

	oapp "github.com/nickZFZ/Sydom/examples/orderservice/app"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/stretchr/testify/require"
)

func TestCatalogPermissions_DeclaresFour(t *testing.T) {
	got := oapp.CatalogPermissions()
	codes := map[string]bool{}
	for _, p := range got {
		codes[p.Code] = true
		require.Equal(t, "order", p.Resource)
	}
	require.True(t, codes["order:read"] && codes["order:write"] && codes["order:delete"] && codes["order:export"])
	require.Len(t, got, 4)
}

type fakeReporter struct{ got []sydom.Permission }

func (f *fakeReporter) ReportPermissions(_ context.Context, ps []sydom.Permission) (sydom.ReportResult, error) {
	f.got = ps
	return sydom.ReportResult{Upserted: 1, Skipped: 3}, nil
}

func TestReportCatalog_ReportsAll(t *testing.T) {
	r := &fakeReporter{}
	oapp.ReportCatalog(context.Background(), r) // fail-soft，无返回
	require.Len(t, r.got, 4)
}

// errReporter 模拟上报端不可用（如 Sidecar 未就绪）。
type errReporter struct{}

func (errReporter) ReportPermissions(_ context.Context, _ []sydom.Permission) (sydom.ReportResult, error) {
	return sydom.ReportResult{}, errors.New("sidecar unavailable")
}

// fail-soft 核心承诺：reporter 报错时 ReportCatalog 既不 panic 也不向上抛错，正常返回。
func TestReportCatalog_FailSoft(t *testing.T) {
	require.NotPanics(t, func() {
		oapp.ReportCatalog(context.Background(), errReporter{})
	})
}
