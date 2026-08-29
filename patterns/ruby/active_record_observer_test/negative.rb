class Order
  def after_save(order)
    order.notify!
  end
end
