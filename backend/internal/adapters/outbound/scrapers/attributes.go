package scrapers

import (
	"strconv"
	"strings"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func numericFact(value string) (float64, bool) {
	v := strings.TrimSpace(value)
	if strings.HasPrefix(v, "-") {
		return 0, false
	}
	if strings.Contains(v, "نامشخص") || strings.Contains(v, "نامعلوم") || strings.Contains(v, "توافق") {
		return 0, false
	}
	if v == "صفر" || v == "صفر کیلومتر" {
		return 0, true
	}
	groups := numericGroup.FindAllString(v, -1)
	if len(groups) != 1 {
		return 0, false
	}
	n, err := strconv.ParseFloat(NormalizePersianDigits(groups[0]), 64)
	if strings.Contains(v, "میلیارد") {
		n *= 1e9
	} else if strings.Contains(v, "میلیون") {
		n *= 1e6
	} else if strings.Contains(v, "هزار") {
		n *= 1000
	}
	return n, err == nil
}

func applyAttribute(l *vehicle.NormalizedVehicleListing, label, value string) {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if label == "" || value == "" {
		return
	}
	key := "source:" + searchText(label)
	switch searchText(label) {
	case "کارکرد", "کیلومتر", "کارکرد مسافت":
		key = "mileageKm"
		if n, ok := numericFact(value); ok {
			l.MileageKm = n
		}
	case "مدل سال تولید", "سال تولید", "سال تولید چهار رقمی", "سال ساخت":
		key = "year"
		if n, ok := numericFact(value); ok && n >= 1300 && n <= 2100 {
			l.Year = int(n)
		}
	case "قیمت", "قیمت نقدی":
		key = "priceToman"
		if strings.Contains(value, "تومان") {
			if n, ok := numericFact(value); ok {
				l.PriceToman = n
			}
		}
	case "رنگ", "رنگ بدنه":
		key = "colorFa"
		l.ColorFa = value
	case "گیربکس", "نوع گیربکس":
		key = "gearbox"
		l.Gearbox = value
	case "سوخت", "نوع سوخت":
		key = "fuelType"
		l.FuelType = value
	case "برند و مدل":
		key = "brandModel"
		l.BrandModel = value
	case "وضعیت بدنه":
		key = "bodyStatus"
		l.BodyStatus = MapBodyStatus(value)
	case "وضعیت فنی موتور", "وضعیت موتور":
		key = "technicalStatus"
		l.TechnicalStatus = value
	case "وضعیت سند و مدارک", "وضعیت سند":
		key = "documentStatus"
		l.DocumentStatus = value
	case "حجم موتور":
		key = "engineVolumeCc"
		if n, ok := numericFact(value); ok {
			l.EngineVolumeCc = n
		}
	}
	for i, old := range l.Attributes {
		if old.Label == label || (old.Key == key && !strings.HasPrefix(key, "source:")) {
			l.Attributes[i] = vehicle.SourceAttribute{Key: key, Label: label, Value: value}
			return
		}
	}
	l.Attributes = append(l.Attributes, vehicle.SourceAttribute{Key: key, Label: label, Value: value})
}
