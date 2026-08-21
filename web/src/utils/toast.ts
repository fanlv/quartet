import './toast.css';

// Single shared toast: a new call removes the previous node so rapid
// clicks (copy path, copy content) never stack overlapping bubbles.
export function showToast(message: string) {
  const existing = document.querySelector('.copy-toast');
  if (existing) {
    existing.remove();
  }

  const toast = document.createElement('div');
  toast.className = 'copy-toast';
  toast.textContent = message;
  document.body.appendChild(toast);

  setTimeout(() => {
    toast.classList.add('show');
  }, 10);

  setTimeout(() => {
    toast.classList.remove('show');
    setTimeout(() => toast.remove(), 300);
  }, 2000);
}
