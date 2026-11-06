// Applies the saved theme before the page paints.
try { var theme = localStorage.getItem("hub-theme"); if (theme) document.documentElement.dataset.theme = theme; } catch (e) {}
