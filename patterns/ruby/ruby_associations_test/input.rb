class Category < ApplicationRecord
  has_many :deliverables
  belongs_to :owner
  has_one :profile, class_name: "UserProfile"
end
