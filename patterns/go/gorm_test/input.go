//go:build ignore

package main

func connect(dsn string) *gorm.DB {
	db, _ := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	return db
}

func loadUser(db *gorm.DB, id int) (User, error) {
	var user User
	err := db.Where("id = ?", id).First(&user).Error
	return user, err
}

func listUsers(db *gorm.DB) []User {
	var users []User
	db.Find(&users)
	return users
}

func saveUser(db *gorm.DB, u *User) {
	db.Create(u2)
	db.Save(&u.Profile)
	db.Delete(&stale)
}
