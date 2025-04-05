package browserhandler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/njasm/marionette_client"
)

type BrowserHandler struct {
	log slog.Logger
}

func NewBrowserHandler(l *slog.Logger) (*BrowserHandler, error) {
	return &BrowserHandler{
		log: *l.With("operation", "BrowserHandler"),
	}, nil
}

func (h *BrowserHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	req := map[string]string{}
	if err = json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	url, ok := req["url"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = NavigateTo(url)
	if err != nil {
		h.log.Error("failed to connect to browser", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
	}

	w.WriteHeader(http.StatusOK)
}

func NavigateTo(url string) error {
	client := marionette_client.NewClient()

	err := client.Connect("", 0)
	if err != nil {
		return err
	}

	_, err = client.NewSession("", nil)
	if err != nil {
		return err
	}

	client.Navigate(url)
	client.DeleteSession()
	return nil
}
