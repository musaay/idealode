package pipeline

import (
	"context"
	"log"
	"unicode"

	"github.com/musaay/idealode/backend/internal/profanity"
	"github.com/musaay/idealode/backend/internal/store"
)

// ScrubResult, ScrubQuotes'un özet sonucu (#100 geriye dönük temizlik).
type ScrubResult struct {
	CardsAffected        int // en az bir satırı düşen kart sayısı
	QuoteLinesDropped    int // toplam düşen example_quotes satırı
	EvidenceLinesDropped int // toplam düşen local_evidence satırı
}

// ScrubQuotes, TÜM kartların example_quotes ve local_evidence alanlarını
// profanity.Filter'dan geçirir (#100). Yeni yazma yollarındaki (InsertIdea
// vb.) store yazım sınırı guard'ı bu komuttan BAĞIMSIZ zaten çalışıyor —
// scrub-quotes yalnız o guard'tan ÖNCE yazılmış eski kartlar için geriye
// dönük temizlik yapar.
//
// dryRun=true iken HİÇBİR ŞEY YAZILMAZ: yalnız kaç kartta kaç satırın
// düşeceği ve düşecek örnek satırlar (MASKELENMİŞ — log'a küfürün kendisi
// asla yazılmaz) loglanır. dryRun=false iken tüm güncellemeler TEK
// transaction'da yazılır (store.ApplyQuoteScrub). İdempotent: ikinci koşuda
// tüm kartlar zaten temiz olduğundan hiçbir satır düşmez, hiçbir yazma
// olmaz.
func ScrubQuotes(ctx context.Context, st *store.Store, dryRun bool) (ScrubResult, error) {
	rows, err := st.ScrubQuoteRows(ctx)
	if err != nil {
		return ScrubResult{}, err
	}

	var result ScrubResult
	var writes []store.ScrubQuoteWrite
	for _, r := range rows {
		newQuotes := profanity.Filter(r.ExampleQuotes)
		newEvidence := profanity.Filter(r.LocalEvidence)
		droppedQuotes := len(r.ExampleQuotes) - len(newQuotes)
		droppedEvidence := len(r.LocalEvidence) - len(newEvidence)
		if droppedQuotes == 0 && droppedEvidence == 0 {
			continue
		}
		result.CardsAffected++
		result.QuoteLinesDropped += droppedQuotes
		result.EvidenceLinesDropped += droppedEvidence

		if dryRun {
			logDroppedLines(r.ID, r.Title, "örnek alıntı", r.ExampleQuotes)
			logDroppedLines(r.ID, r.Title, "yerel kanıt", r.LocalEvidence)
			continue
		}
		writes = append(writes, store.ScrubQuoteWrite{ID: r.ID, ExampleQuotes: newQuotes, LocalEvidence: newEvidence})
	}

	if !dryRun && len(writes) > 0 {
		if err := st.ApplyQuoteScrub(ctx, writes); err != nil {
			return result, err
		}
	}

	if dryRun {
		log.Printf("scrub-quotes (dry-run): %d kart etkilenecek, %d örnek alıntı + %d yerel kanıt satırı düşecek — hiçbir şey yazılmadı",
			result.CardsAffected, result.QuoteLinesDropped, result.EvidenceLinesDropped)
	} else {
		log.Printf("scrub-quotes: %d kart temizlendi, %d örnek alıntı + %d yerel kanıt satırı düştü",
			result.CardsAffected, result.QuoteLinesDropped, result.EvidenceLinesDropped)
	}
	return result, nil
}

// logDroppedLines, dry-run modunda düşecek satırları MASKELENMİŞ loglar —
// yalnız profanity.Contains true dönen (düşecek) satırlar için, log'a
// asla küfürün kendisi yazılmaz.
func logDroppedLines(ideaID int64, title, label string, lines []string) {
	for _, l := range lines {
		if !profanity.Contains(l) {
			continue
		}
		log.Printf("scrub-quotes (dry-run): kart %d (%q) %s satırı düşecek: %s", ideaID, title, label, maskLine(l))
	}
}

// maskLine, loglamak için s'yi maskeler: her kelimenin ilk harfi kalır,
// geri kalanı '*' olur (boşluk/noktalama olduğu gibi korunur) — PO hangi
// satırın düştüğünü ayırt edebilsin diye iz kalır, ama küfürün kendisi
// asla log'a yazılmaz (#100 NOT).
func maskLine(s string) string {
	runes := []rune(s)
	out := make([]rune, len(runes))
	wordStart := true
	for i, r := range runes {
		switch {
		case unicode.IsSpace(r):
			out[i] = r
			wordStart = true
		case wordStart:
			out[i] = r
			wordStart = false
		default:
			out[i] = '*'
		}
	}
	return string(out)
}
