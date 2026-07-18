$.ajax('/api/users');
$.get('/api/users');
$('#list').html('<li>item</li>');
$('#btn').on('click', handleClick);
$.ajax({url: '/save', method: 'POST'});
$(document).on('click', '.item', handleItem);
$('.btn').click(handleItem);
