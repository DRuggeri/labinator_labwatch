package browserhandler

import (
	"log/slog"
	"net/http"
	"net/url"

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
	u := r.URL.Query().Get("url")
	if u == "" {
		h.log.Info("no URL provided")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if _, err := url.Parse(u); err != nil {
		h.log.Info("invalid URL provided", "error", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err := NavigateTo(u)
	if err != nil {
		h.log.Info("failed to connect to browser", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
	}

	h.log.Info("navigated", "url", u)
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
