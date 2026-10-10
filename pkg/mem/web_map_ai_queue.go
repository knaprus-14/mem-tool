package mem

import (
	"context"
	"database/sql"
)

// Keep queue reporting and the worker's claim order identical. A ready source
// map still needs automatic publication; an ordinary ready preview does not.
const webMapAIProcessable = `(status IN ('queued','running') OR (status='ready' AND error='' AND json_extract(payload,'$.source_overview')=1))`

type WebMapAIQueueHead struct {
	MapID    string `json:"map_id"`
	MapTitle string `json:"map_title"`
	Status   string `json:"status"`
	Phase    string `json:"phase"`
	Current  int    `json:"current"`
	Total    int    `json:"total"`
}

// Position describes only this actor's map AI queue in this database. Other
// databases, import and index jobs can also occupy the shared worker.
type WebMapAIQueue struct {
	Position int                `json:"position"`
	Ahead    int                `json:"ahead"`
	Pending  int                `json:"pending"`
	Head     *WebMapAIQueueHead `json:"head,omitempty"`
}

func addWebMapAIQueue(ctx context.Context, tx *sql.Tx, owner string, items []WebMapAITask) error {
	waiting := false
	for _, task := range items {
		waiting = waiting || task.Status == "queued"
	}
	if !waiting {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `WITH pending AS (
 SELECT id,map_id,status,phase,current,total,created FROM web_map_ai_tasks
 WHERE owner=? AND `+webMapAIProcessable+`)
 SELECT t.id,t.map_id,CASE WHEN m.deleted_at='' THEN substr(m.title,1,200) ELSE '' END,
 t.status,t.phase,t.current,t.total FROM pending t
 LEFT JOIN mind_maps m ON m.id=t.map_id ORDER BY t.created,t.id LIMIT 1001`, owner)
	if err != nil {
		return err
	}
	defer rows.Close()
	positions := map[string]int{}
	var head WebMapAIQueueHead
	for rows.Next() {
		var id string
		var current WebMapAIQueueHead
		var title sql.NullString
		if err := rows.Scan(&id, &current.MapID, &title, &current.Status, &current.Phase, &current.Current, &current.Total); err != nil {
			return err
		}
		current.MapTitle = title.String
		if len(positions) == 0 {
			head = current
		}
		positions[id] = len(positions) + 1
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(positions) > 1000 {
		return ErrWebMapAILimit
	}
	for i := range items {
		if position := positions[items[i].ID]; items[i].Status == "queued" && position > 0 {
			items[i].Queue = &WebMapAIQueue{Position: position, Ahead: position - 1, Pending: len(positions)}
			if position > 1 {
				copy := head
				items[i].Queue.Head = &copy
			}
		}
	}
	return nil
}
