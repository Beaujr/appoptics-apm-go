package ao

import (
	"testing"

	"github.com/appoptics/appoptics-apm-go/v1/ao/internal/reporter"
	"github.com/stretchr/testify/assert"
	"gopkg.in/mgo.v2/bson"
)

func TestErrorSpec(t *testing.T) {
	r := reporter.SetTestReporter()

	tr := NewTrace("test")
	tr.Error("testClass", "testMsg")
	tr.End()

	r.Close(3)

	var foundErrEvt = false
	for _, evt := range r.EventBufs {
		m := make(map[string]interface{})
		bson.Unmarshal(evt, m)
		if m["Label"] != reporter.LabelError {
			continue
		}
		foundErrEvt = true
		// check error spec
		assert.Equal(t, reporter.LabelError, m["Label"])
		assert.Equal(t, "test", m["Layer"])
		assert.Equal(t, "error", m["Spec"])
		assert.Equal(t, "testMsg", m["ErrorMsg"])
		assert.Contains(t, m, "Timestamp_u")
		assert.Contains(t, m, "X-Trace")
		assert.Contains(t, m, "Backtrace")
		assert.Equal(t, "1", m["_V"])
		assert.Equal(t, "testClass", m["ErrorClass"])
		assert.IsType(t, "", m["Backtrace"])
	}
	assert.True(t, foundErrEvt)
}
