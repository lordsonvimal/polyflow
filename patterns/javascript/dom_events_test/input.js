btn.addEventListener('click', handleClick);
btn.removeEventListener('click', handleClick);
btn.onclick = handleClick;
input.oninput = (e) => validate(e.target.value);
