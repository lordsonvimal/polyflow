"""Django URL configuration."""
from django.urls import path, re_path
from . import views

urlpatterns = [
    path("users/", views.user_list),
    path("users/<int:pk>/", views.user_detail),
    re_path(r"^archive/(?P<year>[0-9]{4})/", views.archive),
]
