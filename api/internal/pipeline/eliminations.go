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
// criterion/reason/detail/check boş (TrimSpace sonrası "") ya da criterion
// "none" ise NULL yazılır — yalnız stage=distinctiveness satırlarında
// gerçek bir K1-K4 değeri, yalnız stage=blocking_lens/distinctiveness
// satırlarında gerçek bir check (mercek adı) beklenir.
//
// check (#164): elemeyi yapan merceğin adı (gateOutcome.Check) —
// incoherent_theme/vendor_internal gibi mercek-dışı elemelerde "" geçilir,
// NULL yazılır. verdicts: o ana kadar yapılan TÜM mercek çağrılarının
// kaydı — nil ise boş diziye ([]store.LensVerdict{}) indirgenir (ASLA
// NULL/nil yazılmaz, CLAUDE.md nil-slice tuzağı ile AYNI ilke).
func recordElimination(ctx context.Context, st *store.Store, stage, subject, verdict, criterion, reason, detail, check string, verdicts []store.LensVerdict) {
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
	if c := strings.TrimSpace(check); c != "" {
		e.Check = &c
	}
	if verdicts == nil {
		verdicts = []store.LensVerdict{}
	}
	e.Verdicts = verdicts
	if _, err := writeElimination(ctx, st, e); err != nil {
		log.Printf("eliminations: %s/%q kaydı HATA: %v — pipeline devam ediyor", stage, subject, err)
	}
}
