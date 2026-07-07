package obs

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogCtx_RoundTrip(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := With(context.Background(), l)
	require.Same(t, l, From(ctx))
}

func TestLogCtx_DefaultNotNil(t *testing.T) {
	require.NotNil(t, From(context.Background()))
}
