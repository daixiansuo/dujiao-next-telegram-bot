package telegram

import (
	"fmt"
	"html"
	"strings"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/go-telegram/bot/models"
)

func categoryMenu(categories []dujiao.Category, parentID uint, locale string) (string, models.InlineKeyboardMarkup, bool) {
	byID := make(map[uint]dujiao.Category, len(categories))
	hasChildren := make(map[uint]bool)
	for _, category := range categories {
		byID[category.ID] = category
		hasChildren[category.ParentID] = true
	}
	var path []string
	seen := make(map[uint]bool)
	for id := parentID; id != 0; {
		category, ok := byID[id]
		if !ok || seen[id] {
			return "", models.InlineKeyboardMarkup{}, false
		}
		seen[id] = true
		path = append([]string{html.EscapeString(category.Name)}, path...)
		id = category.ParentID
	}
	var choices []models.InlineKeyboardButton
	for _, category := range categories {
		if category.ParentID != parentID {
			continue
		}
		label := category.Name
		if category.ProductCount > 0 {
			label += fmt.Sprintf(" (%d)", category.ProductCount)
		}
		callback := fmt.Sprintf("cat:%d:1", category.ID)
		if hasChildren[category.ID] {
			callback = fmt.Sprintf("cats:%d", category.ID)
		}
		choices = append(choices, models.InlineKeyboardButton{Text: label, CallbackData: callback})
	}
	rows := compactActionRows(choices)
	footer := []models.InlineKeyboardButton{{Text: tr(locale, "全部商品", "全部商品", "All products"), CallbackData: fmt.Sprintf("cat:%d:1", parentID)}}
	if parentID != 0 {
		back := "cats"
		if parent := byID[parentID].ParentID; parent != 0 {
			back = fmt.Sprintf("cats:%d", parent)
		}
		footer = append(footer, models.InlineKeyboardButton{Text: tr(locale, "‹ 上级分类", "‹ 上級分類", "‹ Parent category"), CallbackData: back})
	}
	footer = append(footer, models.InlineKeyboardButton{Text: tr(locale, "‹ 返回首页", "‹ 返回首頁", "‹ Home"), CallbackData: "home"})
	rows = append(rows, compactActionRows(footer)...)
	text := tr(locale, "请选择商品分类：", "請選擇商品分類：", "Choose a product category:")
	if len(path) > 0 {
		text = "<b>" + strings.Join(path, " › ") + "</b>\n\n" + text
	}
	return text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, true
}

func categoryBackCallback(categories []dujiao.Category, categoryID uint) string {
	var parentID uint
	for _, category := range categories {
		if category.ParentID == categoryID {
			return fmt.Sprintf("cats:%d", categoryID)
		}
		if category.ID == categoryID {
			parentID = category.ParentID
		}
	}
	if parentID != 0 {
		return fmt.Sprintf("cats:%d", parentID)
	}
	return "cats"
}

func productCategoryCallback(product dujiao.ProductDetail) string {
	return fmt.Sprintf("cat:%d:1", product.CategoryID)
}
