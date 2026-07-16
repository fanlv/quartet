import { useEffect } from 'react';
import { completeBoot } from '../utils/boot';

export function BootComplete() {
  useEffect(() => {
    completeBoot();
  }, []);
  return null;
}
