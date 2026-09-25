package pipeline

import (
	"context"
	"fmt"
	"log"

	"github.com/musaay/idealode/backend/internal/store"
)

// RethemeResult, Retheme'in özet sonucu (#136).
type RethemeResult struct {
	Resolved  int // bu koşuda temasından çözülen gönderi sayısı
	Themes    int // etkilenen FARKLI eski tema sayısı
	Remaining int // hedef kümede kalan (bu partide çözülmeyen) gönderi sayısı
}

// Retheme, eski tip temalardaki (theme_name == domain_tag ya da domain_tag
// NULL), frekansı cfg.MinThemeEvidence'i geçmiş ama hiç karta dönüşmemiş
// gönderileri temalarından çözer (#136). Yeni kümeleme kodu İÇERMEZ —
// GroupThemes bir sonraki koşuda UnthemedAnalyses ile bu gönderileri
// temasız bulup LLM kümelemesinden geçirir; burada yalnız theme_posts bağı
// silinir. limit ZORUNLU (>0) — çağıran (main.go) doğrular, burada da
// savunmacı olarak tekrar kontrol edilir.
func Retheme(ctx context.Context, st *store.Store, minEvidence, limit int) (RethemeResult, error) {
	if limit <= 0 {
		return RethemeResult{}, fmt.Errorf("--limit zorunlu ve > 0 olmalı, geldi: %d", limit)
	}

	total, err := st.RethemeCandidateCount(ctx, minEvidence)
	if err != nil {
		return RethemeResult{}, fmt.Errorf("hedef küme sayımı: %w", err)
	}
	if total == 0 {
		// İdempotent: hedef küme boşsa hata vermez.
		return RethemeResult{}, nil
	}

	resolved, themes, err := st.RethemeResolve(ctx, minEvidence, limit)
	if err != nil {
		return RethemeResult{}, err
	}

	remaining := total - resolved
	if remaining < 0 {
		// Savunmacı: sayım ve çözüm arasında (aynı süreçte, iki ayrı sorgu)
		// teorik bir yarış olursa negatif "kalan" göstermek yerine 0'a kelepçele.
		remaining = 0
	}
	return RethemeResult{Resolved: resolved, Themes: themes, Remaining: remaining}, nil
}

// RethemeDryRun, hiçbir şey YAZMADAN hedef kümeyi Türkçe log satırlarıyla
// raporlar: kaç gönderinin kaç eski temadan çözüleceği, kalan, bunlardan
// kaçının UnthemedAnalyses kriterini sağlamayacağı (yeniden kümelenemeyecek
// olanlar, spec edge case) ve ilk 10 temanın adı/gönderi sayısı.
func RethemeDryRun(ctx context.Context, st *store.Store, minEvidence, limit int) error {
	if limit <= 0 {
		return fmt.Errorf("--limit zorunlu ve > 0 olmalı, geldi: %d", limit)
	}

	total, err := st.RethemeCandidateCount(ctx, minEvidence)
	if err != nil {
		return fmt.Errorf("hedef küme sayımı: %w", err)
	}

	// #189: eski tip temalardaki, ama artık GroupThemes'ten geçmiş (linked_at
	// DOLU) bağların sayısı — kuyruğun neden sıfıra indiğini/inmediğini
	// göstermek için hedef küme boş olsa bile raporlanır.
	alreadyReclustered, err := st.RethemeAlreadyReclusteredCount(ctx, minEvidence)
	if err != nil {
		return fmt.Errorf("zaten kümelenmiş bağ sayımı: %w", err)
	}
	log.Printf("retheme (dry-run): %d gönderi LLM kümelemesinden zaten geçti, tekrar alınmaz", alreadyReclustered)

	if total == 0 {
		log.Printf("retheme (dry-run): hedef küme boş, çözülecek gönderi yok")
		return nil
	}

	targets, err := st.RethemeCandidates(ctx, minEvidence, limit)
	if err != nil {
		return fmt.Errorf("hedef küme (limit uygulanmış): %w", err)
	}

	resolved := len(targets)
	themeSet := make(map[int64]bool, resolved)
	postIDs := make([]int64, resolved)
	for i, t := range targets {
		themeSet[t.ThemeID] = true
		postIDs[i] = t.PostID
	}

	wontRecluster, err := st.RethemeWontReclusterCount(ctx, postIDs)
	if err != nil {
		return fmt.Errorf("yeniden kümelenemeyecek sayımı: %w", err)
	}

	topThemes, err := st.RethemeTopThemes(ctx, minEvidence, 10)
	if err != nil {
		return fmt.Errorf("ilk 10 tema: %w", err)
	}

	remaining := total - resolved
	if remaining < 0 {
		remaining = 0
	}

	log.Printf("retheme (dry-run): %d gönderi %d eski temadan çözülecek, kalan %d, bunlardan %d'i yeniden kümelenemeyecek (UnthemedAnalyses kriterini sağlamıyor)",
		resolved, len(themeSet), remaining, wontRecluster)
	for _, t := range topThemes {
		log.Printf("retheme (dry-run): tema %q — %d gönderi", t.Name, t.Frequency)
	}
	return nil
}
