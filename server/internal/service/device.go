package service

import (
	"context"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

type DeviceService struct{ *Services }

func (svc *DeviceService) List(ctx context.Context, userID, currentDeviceID string) ([]DeviceView, error) {
	rows, err := svc.Q.ListDevicesByUser(ctx, &userID)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	views := make([]DeviceView, 0, len(rows))
	for _, d := range rows {
		views = append(views, NewDeviceView(d, currentDeviceID))
	}
	return views, nil
}

type ReleaseDeviceInput struct {
	UserID    string
	DeviceID  string
	Reason    string
	ActorType string
	ActorID   string
	IP        string
}
