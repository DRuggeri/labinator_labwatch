package talos_test

import (
	"testing"

	"github.com/DRuggeri/labwatch/watchers/talos"
	"github.com/stretchr/testify/assert"
)

func TestWatcher(t *testing.T) {
	w, err := talos.NewTalosWatcher("/root/talos/talosconfig", "koobs")
	assert.NoError(t, err)
	assert.NotNil(t, w)

	w.TestIt()
}
