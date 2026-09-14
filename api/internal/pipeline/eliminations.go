package pipeline

import (
	"context"
	"log"
	"strings"

	"github.com/musaay/idealode/api/internal/store"
)

// eliminationReasonLimit/eliminationDetailLimit: eliminations.reason ve
// eliminations.detail'in kırpma sınırı (rune, #138).
const eliminationReasonLimit = 500
const eliminationDetailLimit = 500

// eliminationWriter, eliminations tablosuna yazan fonksiyonu soyutlar —
// testte hata döndüren bir sahteyle değiştirilebilsin diye (best-effort
// davranışın pipeline'ı durdurmadığını doğrulamak için; seeds.go'daki
// fetchTrendingRepoMeta deseniyle AYNI: paket seviyesinde değişken, public
// imza değişmez).
type eliminationWriter func(ctx context.Context, st *store.Store, e store.Elimination) (int64, error)

var writeElimination eliminationWriter = func(ctx context.Context, st *store.Store, e store.Elimination) (int64, error) {
	return st.InsertElimination(ctx, e)
}

// recordElimination, eliminations tablosuna BEST-EFFORT tek satır yazar
// (#138): yazım hatası pipeline'ı ASLA durdurmaz, tek satır Türkçe log
// düşer — çağıranlar dönüş değerini kontrol ETMEZ (fonksiyonun kendisi
// hata döndürmez), bu yüzden bir yazım hatası akışı hiçbir şekilde
// etkileyemez.
//
// criterion/reason/detail boş (TrimSpace sonrası "") ya da criterion "none"
// ise NULL yazılır — yalnız stage=distinctiveness satırlarında gerçek bir
// K1-K4 değeri beklenir.
func recordElimination(ctx context.Context, st *store.Store, stage, subject, verdict, criterion, reason, detail string) {
	e := store.Elimination{Stage: stage, Subject: subject, Verdict: verdict}
	if c := strings.TrimSpace(criterion); c != "" && c != "none" {
		e.Criterion = &c
	}
	if r := clip(strings.TrimSpace(reason), eliminationReasonLimit); r != "" {
		e.Reason = &r
	}
	if d := clip(strings.TrimSpace(detail), eliminationDetailLimit); d != "" {
		e.Detail = &d
	}
	if _, err := writeElimination(ctx, st, e); err != nil {
		log.Printf("eliminations: %s/%q kaydı HATA: %v — pipeline devam ediyor", stage, subject, err)
	}
}
