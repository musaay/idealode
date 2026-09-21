// web — IdeaLode'un salt okunur web arayüzünü sunan tek komutlu binary
// (galeri + kart detayı + sohbet). #178: eskiden `idealode serve`
// alt-komutuydu; artık backend'i import ETMEYEN ayrı bir Go modülü
// (github.com/musaay/idealode/ui). Pipeline çalıştırmaz, veritabanına
// bağlanmaz — kart verisini API_BASE_URL üzerinden apiclient ile okur
// (backend/internal/api, #18). Railway'de `run` cron servisinden ve
// `idealode-api`'den ayrı bir servis olarak koşar.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/musaay/idealode/ui/internal/apiclient"
	"github.com/musaay/idealode/ui/internal/web"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("web: ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		log.Fatalf("%v", err)
	}
}

// run, adres PORT ortam değişkeninden (varsayılan 8080), API adresi
// API_BASE_URL'den (zorunlu) okunur.
func run(ctx context.Context) error {
	base := os.Getenv("API_BASE_URL")
	if base == "" {
		return fmt.Errorf("zorunlu ortam değişkeni eksik: API_BASE_URL")
	}
	client := apiclient.New(base, 5*time.Second)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return web.NewServer(client).ListenAndServe(ctx, ":"+port)
}
