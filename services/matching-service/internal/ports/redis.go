package ports
type DriverRepository interface {
	FindNearbyDrivers(lat,lng float64,radiusKM float64)([]string ,error)
	TryLockDriver(driverID,rideId string)(bool,error)

}