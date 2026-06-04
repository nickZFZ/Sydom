package dataperm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCondition_ValidTree(t *testing.T) {
	c, err := parseCondition(`{"op":"AND","children":[
		{"field":"department","op":"EQ","value":"$user.department"},
		{"field":"status","op":"IN","value":["pending","approved"]}
	]}`)
	require.NoError(t, err)
	require.Equal(t, OpAnd, c.Op)
	require.Len(t, c.Children, 2)
	require.Equal(t, "department", c.Children[0].Field)
}

func TestParseCondition_RejectsBadField(t *testing.T) {
	_, err := parseCondition(`{"field":"dept; DROP TABLE","op":"EQ","value":"x"}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestParseCondition_RejectsUnknownOp(t *testing.T) {
	_, err := parseCondition(`{"field":"a","op":"REGEX","value":"x"}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestParseCondition_RejectsArityMismatch(t *testing.T) {
	_, err := parseCondition(`{"field":"a","op":"IN","value":"notarray"}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
	_, err = parseCondition(`{"field":"a","op":"BETWEEN","value":[1]}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
	_, err = parseCondition(`{"op":"NOT","children":[]}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestParseCondition_RejectsBadJSON(t *testing.T) {
	_, err := parseCondition(`{not json`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestParseCondition_RejectsLogicalNodeWithLeafFields(t *testing.T) {
	_, err := parseCondition(`{"op":"AND","field":"injected","children":[
		{"field":"a","op":"EQ","value":1}
	]}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
	_, err = parseCondition(`{"op":"NOT","value":"x","children":[
		{"field":"a","op":"EQ","value":1}
	]}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}
