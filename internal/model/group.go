package model

// AccountGroup 表示用户自建的账号分组（取代原有标签系统）。
type AccountGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	SortOrder int    `json:"sortOrder"`
	CreatedAt int64  `json:"createdAt"`
}
