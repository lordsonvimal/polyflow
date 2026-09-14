class NotARecord
  # receiver-qualified: not a bare macro call
  x.has_many :things
  # wrong method name shape
  belongs_to_something :y
  has_two :z
end
