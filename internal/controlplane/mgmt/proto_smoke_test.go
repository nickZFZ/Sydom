package mgmt_test

import (
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
)

func TestProtoGenerated(t *testing.T) {
	_ = &adminv1.GrantPermissionRequest{}
	_ = &adminv1.WriteResponse{}
	_ = &adminv1.CreateApplicationRequest{}
	_ = &adminv1.CreateOperatorRequest{}
}
