package console

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/stretchr/testify/require"
)

// builder v2 会产出的 canonical 大写形状必须被引擎接受(此前小写/contains/单层从未测过)。
func TestBuilderV2SerializedShapesAccepted(t *testing.T) {
	shapes := []string{
		`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"}]}`,
		`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"},{"op":"OR","children":[{"field":"status","op":"IN","value":["pending","approved"]},{"op":"NOT","children":[{"field":"archived","op":"EQ","value":true}]}]}]}`,
		`{"field":"amount","op":"BETWEEN","value":[1,100]}`,
		`{"field":"note","op":"IS_NULL"}`,
		`{"field":"name","op":"NOT_LIKE","value":"%x%"}`,
	}
	for _, s := range shapes {
		require.NoError(t, dataperm.ValidateCondition(s), "构建器 v2 形状必须被引擎接受: %s", s)
	}
}
