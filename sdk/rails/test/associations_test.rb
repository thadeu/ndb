# frozen_string_literal: true

require "test_helper"

class Customer < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :name, :string
end

class Product < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :code, :string, index: true
  attribute :name, :string
end

class Item < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :order_id, :string
  attribute :sku, :string
  attribute :pos, :integer

  belongs_to :product, foreign_key: :sku, primary_key: :code
end

class Invoice < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :order_id, :string
  attribute :number, :string
end

class Order < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :customer_id, :string
  attribute :total, :integer

  belongs_to :customer
  has_many :items, sort: { pos: 1 }
  has_one :invoice
end

class AssociationsTest < Minitest::Test
  def setup
    Customer.create!(_id: "c1", name: "Ana")
    Product.create!([{ _id: "p1", code: "A", name: "Mouse" }, { _id: "p2", code: "B", name: "Teclado" }])
    Order.create!([{ _id: "o1", customer_id: "c1", total: 120 }, { _id: "o2", customer_id: "nobody", total: 1 }])
    Item.create!([{ _id: "i1", order_id: "o1", sku: "A", pos: 2 }, { _id: "i2", order_id: "o1", sku: "B", pos: 1 }])
    Invoice.create!(_id: "f1", order_id: "o1", number: "NF-1")
  end

  def teardown
    [Customer, Product, Item, Invoice, Order].each(&:truncate!)
  end

  def test_rails_defaults
    assert_equal "customer_id", Order.reflections["customer"].foreign_key
    assert_equal "Customer", Order.reflections["customer"].class_name
    assert_equal "order_id", Order.reflections["items"].foreign_key
    assert_equal "Item", Order.reflections["items"].class_name
  end

  def test_includes_loads_with_the_rows_in_one_call
    orders = Order.order(:_id).includes(:customer, :invoice, items: :product).to_a
    first = orders.first

    assert_kind_of Customer, first.customer
    assert_equal "Ana", first.customer.name
    assert_equal %w[i2 i1], first.items.map(&:_id)
    assert_equal "Teclado", first.items.first.product.name
    assert_equal "NF-1", first.invoice.number
    assert_nil orders.last.customer
    assert_empty orders.last.items
  end

  def test_without_includes_an_association_loads_on_use
    order = Order.find("o1")

    assert_equal "Ana", order.customer.name
    assert_equal %w[i2 i1], order.items.map(&:_id)
    assert_equal "Mouse", order.items.last.product.name
  end
end
