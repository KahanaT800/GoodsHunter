package crawler

import (
	"net/url"
	"strconv"
	"strings"

	"goodshunter/proto/pb"
)

// BuildMercariURL 构造 Mercari 搜索页面的 URL。
//
// 它将 gRPC 请求中的参数（关键词、价格区间、排序等）转换为 URL 查询字符串。
// 注意：limit 参数不包含在 URL 中，因为它仅用于控制爬虫内部逻辑。
//
// 参数:
//
//	req: gRPC 请求对象
//
// 返回值:
//
//	string: 完整的 Mercari 搜索 URL
func BuildMercariURL(req *pb.FetchRequest) string {
	base := "https://jp.mercari.com/search"
	values := url.Values{}

	if req.GetKeyword() != "" {
		values.Set("keyword", req.GetKeyword())
	}

	// 强制带上在售状态，模拟站点默认行为。
	values.Set("status", "on_sale")

	minPrice := req.GetMinPrice()
	if minPrice > 0 && minPrice < 300 {
		minPrice = 300
	}
	if minPrice > 0 {
		values.Set("price_min", strconv.FormatInt(int64(minPrice), 10))
	}
	if req.GetMaxPrice() > 0 {
		values.Set("price_max", strconv.FormatInt(int64(req.GetMaxPrice()), 10))
	}

	for _, ex := range req.GetExcludeKeywords() {
		if strings.TrimSpace(ex) == "" {
			continue
		}
		values.Add("exclude_keyword", ex)
	}

	// limit 参数仅用于控制爬虫抓取的数量，不作为 URL 参数传递给 Mercari
	// if req.GetLimit() > 0 {
	// 	values.Set("limit", strconv.FormatInt(int64(req.GetLimit()), 10))
	// }

	if sort := mapSortBy(req.GetSort()); sort != "" {
		values.Set("sort", sort)
	}
	if order := mapSortOrder(req.GetOrder()); order != "" {
		values.Set("order", order)
	}

	qs := values.Encode()
	qs = strings.ReplaceAll(qs, "+", "%20")
	return base + "?" + qs
}

// mapSortBy 将 Protobuf 定义的排序枚举映射为 Mercari URL 参数值。
//
// 参数:
//
//	s: 排序枚举值
//
// 返回值:
//
//	string: 对应的 URL 参数字符串（如 "price", "created_time"）
func mapSortBy(s pb.SortBy) string {
	switch s {
	case pb.SortBy_SORT_BY_PRICE:
		return "price"
	case pb.SortBy_SORT_BY_SCORE:
		return "score"
	case pb.SortBy_SORT_BY_NUM_LIKES:
		return "num_likes"
	case pb.SortBy_SORT_BY_CREATED_TIME:
		fallthrough
	default:
		return "created_time"
	}
}

// mapSortOrder 将 Protobuf 定义的排序顺序枚举映射为 Mercari URL 参数值。
//
// 参数:
//
//	o: 排序顺序枚举值
//
// 返回值:
//
//	string: 对应的 URL 参数字符串（"asc" 或 "desc"）
func mapSortOrder(o pb.SortOrder) string {
	switch o {
	case pb.SortOrder_SORT_ORDER_ASC:
		return "asc"
	case pb.SortOrder_SORT_ORDER_DESC:
		fallthrough
	default:
		return "desc"
	}
}
