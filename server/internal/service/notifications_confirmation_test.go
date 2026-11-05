package service_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestFeishuRetriesUnconfirmedDelivery(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	var mu sync.Mutex
	var requests int
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		mu.Lock()
		requests++
		attempt := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			_, _ = io.WriteString(w, `{"code":19024`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	t.Cleanup(receiver.Close)
	channel, _ := f.channel(alice, notify.KindFeishu, receiver.URL, allRunEvents(alice.PersonalProjectID))
	f.endedRun(f.apply(alice, "digest", basicManifest()), store.RunFailed)
	f.dispatch()
	deliveries := f.deliveries(channel.Channel.ID)
	if len(deliveries) != 1 || deliveries[0].Status != store.DeliveryPending || deliveries[0].Attempts != 1 ||
		deliveries[0].HTTPStatus != 200 || deliveries[0].Error == "" || !deliveries[0].DeliveredAt.IsZero() {
		t.Fatalf("unconfirmed delivery = %+v", deliveries)
	}
	id := deliveries[0].ID
	f.clock.Advance(time.Minute)
	f.dispatch()
	deliveries = f.deliveries(channel.Channel.ID)
	if len(deliveries) != 1 || deliveries[0].Status != store.DeliveryDelivered || deliveries[0].Attempts != 2 ||
		deliveries[0].Error != "" || deliveries[0].DeliveredAt.IsZero() || deliveries[0].ID != id {
		t.Fatalf("confirmed delivery = %+v", deliveries)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("requests = %d, want two attempts of the same delivery", requests)
	}
}
