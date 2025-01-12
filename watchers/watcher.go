package watchers

type Watcher interface {
	Watch(control <-chan ControlAction, result chan<- WatchState)
}

type ControlAction string

const ActionStart ControlAction = "start"
const ActionStop ControlAction = "stop"
const ActionPause ControlAction = "pause"

type WatchState struct {
	Error  error
	States map[string]string
	Node   string
}
