package telegram

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func TestCatalogThreeLevelsBrowseAndReturn(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newFormFixture(t, nil)
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		f.categories = []dujiao.Category{
			{ID: 2, Name: "社交<&>"},
			{ID: 10, ParentID: 2, Name: "Facebook"},
			{ID: 11, ParentID: 10, Name: "美国", ProductCount: 1},
			{ID: 12, ParentID: 10, Name: "日本", ProductCount: 2},
			{ID: 20, Name: "邮箱", ProductCount: 1},
		}
		a.showCategories(context.Background(), 42, &models.User{ID: 42}, true, 10)
		k := formKeyboard(t, f)
		if findCallback(k, "cats:2") != "cats:2" || findCallback(k, "cat:11:") != "" || findCallback(k, "cat:20:1") != "cat:20:1" {
			t.Fatal("root categories were flattened")
		}
		formCallback(a, 42, 10, "cats:2")
		k = formKeyboard(t, f)
		if findCallback(k, "cats:10") != "cats:10" || findCallback(k, "cat:2:1") != "cat:2:1" || !strings.Contains(f.markups[len(f.markups)-1], `"callback_data":"cats"`) {
			t.Fatal("second level or root return missing")
		}
		formCallback(a, 42, 10, "cats:10")
		k = formKeyboard(t, f)
		if findCallback(k, "cat:11:1") != "cat:11:1" || findCallback(k, "cats:2") != "cats:2" || !strings.Contains(f.texts[len(f.texts)-1], "社交&lt;&amp;&gt; › Facebook") {
			t.Fatal("third level, breadcrumb or parent return missing")
		}
		f.products = dujiao.ProductList{Items: []dujiao.Product{{ID: 3, Title: "Test", PriceFrom: "10.00", Currency: "CNY"}}, Total: 1, TotalPage: 1}
		formCallback(a, 42, 10, "cat:11:1")
		k = formKeyboard(t, f)
		if findCallback(k, "cats:10") != "cats:10" || findCallback(k, "product:3") != "product:3" {
			t.Fatal("leaf products did not return to their parent")
		}
		request, _ := url.Parse(f.catalogQueries[len(f.catalogQueries)-1])
		if request.Query().Get("category_id") != "11" || request.Query().Get("exact") == "1" {
			t.Fatal("category query excludes descendant products")
		}
		f.product.CategoryID = 11
		formCallback(a, 42, 10, "product:3")
		if findCallback(formKeyboard(t, f), "cat:11:1") != "cat:11:1" {
			t.Fatal("quantity page lost the product category")
		}
		if len(f.orders) != 0 || len(f.previews) != 0 {
			t.Fatal("catalog navigation created an order")
		}
	}
}

func TestCatalogMissingAndCyclicCategoryCannotTrapNavigation(t *testing.T) {
	a, f := newFormFixture(t, nil)
	f.categories = []dujiao.Category{{ID: 1, Name: "Root", ProductCount: 1}, {ID: 7, ParentID: 8}, {ID: 8, ParentID: 7}}
	for _, id := range []uint{999, 7} {
		a.showCategoryLevel(context.Background(), 42, &models.User{ID: 42}, id, true, 10)
		if findCallback(formKeyboard(t, f), "cat:1:1") != "cat:1:1" {
			t.Fatal("invalid category did not return to current roots")
		}
	}
}
