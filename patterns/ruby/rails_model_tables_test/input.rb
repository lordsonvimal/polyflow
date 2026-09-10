class ApplicationRecord < ActiveRecord::Base
  self.abstract_class = true
end

class Widget < ApplicationRecord
end

class Gadget < ApplicationRecord
  self.table_name = "legacy_gadgets"
end
