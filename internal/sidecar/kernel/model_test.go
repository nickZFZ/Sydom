package kernel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildModel_ParsesAndHasSections(t *testing.T) {
	m, err := buildModel()
	require.NoError(t, err)
	for _, sec := range []string{"r", "p", "g", "e", "m"} {
		require.Contains(t, m, sec, "model 缺少段 %q", sec)
	}
}
